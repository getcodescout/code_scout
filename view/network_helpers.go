package view

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"time"

	"github.com/getcodescout/code_scout/internal/domain"
	"github.com/google/uuid"
)

// launchScoped reports whether this list is one app launch rather than a whole
// project. Only the session screen scopes a call list to a session, so the same
// answer decides three things: which columns the table needs, what the offsets
// are measured from, and which page a selection belongs to.
func (d NetworkData) launchScoped() bool { return d.Filter.SessionID != nil }

// launchStart is what a launch-scoped list measures "since launch" from: the
// session's own start, or the earliest call when the session row never arrived,
// which happens for logs uploaded by an SDK older than 1.2.0.
//
// It is a rule rather than a value passed in because the inspector fragment
// rebuilds these rows without ever seeing the session screen. Both paths have
// to reach the same number or the column would shift on every click.
func (d NetworkData) launchStart() time.Time {
	if !d.SessionStart.IsZero() {
		return d.SessionStart
	}
	start, _ := callWindow(d.Calls)
	return start
}

// pageHref is the page address a selection corresponds to — what goes in the
// address bar. fragmentHref is the endpoint that actually answers the click.
// They are kept side by side because they must always agree: the URL pushed and
// the URL that would rebuild the same view.
//
// A launch-scoped selection belongs to the session screen, which carries its
// launch in the path and its own tab in the query. The fragment endpoint is the
// same one either way — it rebuilds the list from the filter, and the filter is
// what says which launch.
func (d NetworkData) pageHref(requestID uuid.UUID, phase string) string {
	if d.launchScoped() {
		q := url.Values{"tab": []string{"network"}}
		setSelection(q, requestID, phase)
		return fmt.Sprintf("/project/%s/session/%s?%s", d.ProjectID, d.Filter.SessionID, q.Encode())
	}
	return fmt.Sprintf("/project/%s/network?%s", d.ProjectID, networkQuery(d.Filter, requestID, phase))
}

func (d NetworkData) fragmentHref(requestID uuid.UUID, phase string) string {
	return fmt.Sprintf("/project/%s/network/inspector?%s", d.ProjectID, networkQuery(d.Filter, requestID, phase))
}

// The filter travels with the selection so a swapped-in detail pane's own links
// still know what the list is filtered to. Without it, clicking a tab would
// push a URL that reloads to an unfiltered list.
func networkQuery(f domain.NetworkFilter, requestID uuid.UUID, phase string) string {
	q := url.Values{}
	if f.Path != "" {
		q.Set("path", f.Path)
	}
	if f.Method != "" {
		q.Set("method", f.Method)
	}
	if f.Status != "" {
		q.Set("status", f.Status)
	}
	if f.SessionID != nil {
		q.Set("session", f.SessionID.String())
	}
	setSelection(q, requestID, phase)
	return q.Encode()
}

// The selection is separate from the filter because the session screen keeps
// its launch in the path and has no filter to carry.
//
// An empty rid= is not the same as no rid at all. Absent means "no opinion",
// and the newest call opens. Present and empty means the inspector was closed
// on purpose, which has to survive a reload or the close button would reopen
// the call you just dismissed.
//
// The phase is `phase` rather than `tab` because the session screen already
// spends `tab` on Logs and Network, and one query string cannot hold two
// parameters of the same name and mean both.
func setSelection(q url.Values, requestID uuid.UUID, phase string) {
	q.Set("rid", "")
	if requestID != uuid.Nil {
		q.Set("rid", requestID.String())
	}
	if phase != "" {
		q.Set("phase", phase)
	}
}

// callWindow is the time span the waterfall is drawn against: the earliest
// start to the latest end across everything on screen. A minimum span keeps a
// burst of calls in the same millisecond from dividing by zero.
func callWindow(calls []domain.NetworkCall) (time.Time, time.Duration) {
	if len(calls) == 0 {
		return time.Time{}, time.Second
	}
	start, end := calls[0].StartedAt, calls[0].EndedAt
	for _, c := range calls {
		if c.StartedAt.Before(start) {
			start = c.StartedAt
		}
		if c.EndedAt.After(end) {
			end = c.EndedAt
		}
	}
	span := end.Sub(start)
	if span < time.Millisecond {
		span = time.Millisecond
	}
	return start, span
}

// waterfallBar is where one call's bar sits, as percentages of the window.
//
// A pending call is drawn from its start to the end of the window, because
// that is what "still going" looks like — a one-pixel stub would read as a call
// that finished instantly.
func waterfallBar(call domain.NetworkCall, start time.Time, span time.Duration) (offset float64, width float64) {
	if span <= 0 {
		return 0, 100
	}
	offset = float64(call.StartedAt.Sub(start)) / float64(span) * 100
	if offset < 0 {
		offset = 0
	}
	if offset > 100 {
		offset = 100
	}

	if call.State() == domain.CallPending {
		width = 100 - offset
	} else {
		width = float64(call.Duration()) / float64(span) * 100
	}
	// Always visible: a fast call in a long window rounds to nothing.
	//
	// A percentage is only half of that guarantee, because it is a percentage of
	// a column this file does not control: at the 62px the column used to
	// collapse to, 2% was 1.2px. The other half is `min-w-[3px]` on the bar
	// itself, which is in pixels and cannot be undone by a layout change.
	if width < 2 {
		width = 2
	}
	if offset+width > 100 {
		width = 100 - offset
	}
	return offset, width
}

// durationLabel keeps three significant figures where they matter: sub-second
// calls in milliseconds, anything longer in seconds.
func durationLabel(call domain.NetworkCall) string {
	if call.State() == domain.CallPending {
		return "—"
	}
	d := call.Duration()
	switch {
	case d < time.Millisecond:
		return "<1ms"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	default:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
}

func methodLabel(method *string) string {
	if method == nil || *method == "" {
		return "—"
	}
	return *method
}

// Methods are coloured by how much they can break: reads are quiet, writes are
// warmer, a delete is the one you want to notice in a list.
func methodClass(method *string) string {
	if method == nil {
		return "text-cs-muted"
	}
	switch *method {
	case "GET", "HEAD", "OPTIONS":
		return "text-[#3FB950]"
	case "POST":
		return "text-[#D29922]"
	case "PUT", "PATCH":
		return "text-[#0084FF]"
	case "DELETE":
		return "text-[#F85149]"
	default:
		return "text-cs-muted"
	}
}

func callsSummary(calls []domain.NetworkCall) string {
	if len(calls) == 0 {
		return ""
	}
	var failed int
	for _, c := range calls {
		if c.Failed() {
			failed++
		}
	}
	label := fmt.Sprintf("%d calls", len(calls))
	if len(calls) == 1 {
		label = "1 call"
	}
	if failed == 0 {
		return label
	}
	return fmt.Sprintf("%s · %d failed", label, failed)
}

// AvailableTabs is the tabs to offer.
//
// Chrome's split, not the SDK's three phases. The phases are how the call was
// captured — request, then response or error — and tabbing by them put a wall
// of twenty-five response headers in front of the response body, which is the
// thing you opened the call to read. Headers are their own tab and hold both
// directions at once, which is also the only way to compare them.
//
// A tab that could only ever say "nothing here" is not offered: no Payload
// without a request body, no Response until the call came back.
func AvailableTabs(logs []domain.Log) []string {
	phases, _ := domain.SplitCallLogs(logs)
	if len(phases) == 0 {
		return nil
	}

	out := []string{"headers"}
	if req := phaseLog(phases, "request"); req != nil {
		if rawField(decodeMeta(req.Metadata), "body") != "" {
			out = append(out, "payload")
		}
	}
	if phaseLog(phases, "response") != nil {
		out = append(out, "response")
	}
	if phaseLog(phases, "error") != nil {
		out = append(out, "error")
	}
	return out
}

// DefaultTab is the tab to open on. An error is why you clicked, so it wins;
// otherwise the response body, which is what you usually came to read. Headers
// last, because they are reference rather than the answer.
func DefaultTab(phases []domain.Log) string {
	available := AvailableTabs(phases)
	for _, want := range []string{"error", "response", "payload", "headers"} {
		for _, tab := range available {
			if tab == want {
				return tab
			}
		}
	}
	return ""
}

// TabAvailable keeps a tab= in the query string from selecting a tab this call
// does not have, which would render an empty pane with nothing highlighted.
func TabAvailable(phases []domain.Log, tab string) bool {
	for _, t := range AvailableTabs(phases) {
		if t == tab {
			return true
		}
	}
	return false
}

// phaseLog takes every log with the call's request id, the app's own included,
// so it answers only from network logs: the same split the "Logged by the app"
// section makes, or one log could be drawn as both.
func phaseLog(logs []domain.Log, tab string) *domain.Log {
	for i := range logs {
		if logs[i].IsNetworkCall && logs[i].CallPhase != nil && string(*logs[i].CallPhase) == tab {
			return &logs[i]
		}
	}
	return nil
}

// noCallsStored is what an empty list says beside a call that was not
// captured. It agrees with the pane: no calls were stored, which is not the
// same as none being made.
func noCallsStored(d NetworkData) string {
	switch {
	case d.launchScoped():
		return "No network calls were stored for this launch."
	case !d.Filter.IsEmpty():
		return "Nothing matches that filter."
	default:
		return "No network calls were stored."
	}
}

// LinkedLogLimit is how many of the app's logs the handlers load for the
// section, which lists them all and leaves the rest to the link. The inspector
// is a fixed height, and every row here is a row less of the response body
// underneath.
const LinkedLogLimit = 3

// linkedLogsShown is the heading's count, given only when the section does not
// list every one of the app's logs. The count is of the app's logs alone, so it
// stays off the link: the page the link opens lists the phases as well.
func linkedLogsShown(listed, linkedTotal int) string {
	if linkedTotal <= listed {
		return ""
	}
	return fmt.Sprintf("%d of %d", listed, linkedTotal)
}

// requestLogsHref is the log viewer filtered to one request id, which lists the
// call's phases and every log the app linked to it.
func requestLogsHref(projectID, requestID uuid.UUID) string {
	q := url.Values{"q": []string{domain.SearchFilter{RequestID: &requestID}.Query()}}
	return fmt.Sprintf("/project/%s/logs?%s", projectID, q.Encode())
}

func decodeMeta(raw *json.RawMessage) map[string]any {
	if raw == nil || len(*raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := decodeJSON(*raw, &m); err != nil {
		return nil
	}
	return m
}

// decodeJSON keeps every number as the literal the app sent. A plain decode
// makes each one a float64, which prints 12.0 as 12 and rounds an id above
// 2^53, and the difference between 12.0 and 12 is what a decode failure is
// about.
func decodeJSON(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(v)
}

func stringField(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	if s, ok := meta[key].(string); ok {
		return s
	}
	return ""
}

func mapField(meta map[string]any, key string) map[string]any {
	if meta == nil {
		return nil
	}
	if m, ok := meta[key].(map[string]any); ok {
		return m
	}
	return nil
}

// rawField renders a body whatever shape it arrived in. The SDK sends whatever
// the app sent: a JSON object, a string, or nothing.
func rawField(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	value, ok := meta[key]
	if !ok || value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	pretty, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(pretty)
}

// sortedKV puts headers in a stable order, so the same call inspected twice
// does not shuffle its rows around.
func sortedKV(m map[string]any) []kv {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]kv, 0, len(keys))
	for _, k := range keys {
		out = append(out, kv{Key: k, Value: headerValue(m[k])})
	}
	return out
}

// A header can legitimately be a list — Set-Cookie usually is.
func headerValue(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			parts = append(parts, fmt.Sprintf("%v", item))
		}
		return joinComma(parts)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// RedactedMarker is what the SDK puts in place of a value it stripped. The
// exact string is a contract with the Flutter package's Redactor: change it
// there and the dashboard stops recognising a redaction and prints the marker
// as though it were the header's value.
const RedactedMarker = "[redacted]"

func isRedacted(value string) bool { return value == RedactedMarker }

func tabLabel(tab string) string {
	switch tab {
	case "headers":
		return "Headers"
	case "payload":
		return "Payload"
	case "response":
		return "Response"
	case "error":
		return "Error"
	}
	return tab
}
