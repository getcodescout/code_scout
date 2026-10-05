package view

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/getcodescout/code_scout/internal/domain"
	"github.com/google/uuid"
)

// The logs GetByRequestID hands back for one call, oldest first, the way the
// handlers pass them on: the call's phases and the app's own logs together.

func netLog(rid uuid.UUID, phase string, at time.Time, meta string) domain.Log {
	cp := domain.CallPhase(phase)
	l := domain.Log{
		ID: uuid.New(), Level: "debug", Message: "Network " + phase, TimeStamp: at,
		IsNetworkCall: true, RequestID: &rid, CallPhase: &cp,
	}
	if meta != "" {
		raw := json.RawMessage(meta)
		l.Metadata = &raw
	}
	return l
}

func appLogAbout(rid uuid.UUID, at time.Time, message, errText string) domain.Log {
	l := domain.Log{ID: uuid.New(), Level: "error", Message: message, TimeStamp: at, RequestID: &rid}
	if errText != "" {
		l.Error = &errText
	}
	return l
}

const decodeError = "type 'int' is not a subtype of type 'double' in type cast"

// A GET /v2/cart that answered 200 in 118ms, whose body the app could not read.
func cartCall(extra ...domain.Log) NetworkData {
	at := launchedAt()
	rid := uuid.New()
	method, u, status := "GET", "https://api.test.dev/v2/cart", 200
	selected := domain.NetworkCall{
		RequestID: rid, Method: &method, URL: &u, StatusCode: &status,
		StartedAt: at, EndedAt: at.Add(118 * time.Millisecond),
		HasRequest: true, HasResponse: true,
	}
	logs := []domain.Log{
		netLog(rid, "request", at, `{"method":"GET","url":"https://api.test.dev/v2/cart"}`),
		netLog(rid, "response", at.Add(118*time.Millisecond), `{"status_code":200,"body":{"subtotal":49}}`),
	}
	for _, l := range extra {
		l.RequestID = &rid
		logs = append(logs, l)
	}
	return NetworkData{
		ProjectID: uuid.New(), Calls: []domain.NetworkCall{selected}, Selected: &selected,
		Phases: logs, LinkedTotal: len(extra), Phase: "response",
	}
}

// appLogs is n of the app's logs about one call, a second apart.
func appLogs(n int) []domain.Log {
	var logs []domain.Log
	for i := 1; i <= n; i++ {
		logs = append(logs, appLogAbout(uuid.Nil, launchedAt().Add(time.Duration(i)*time.Second), fmt.Sprintf("app log number %d", i), ""))
	}
	return logs
}

func TestTheInspectorListsTheAppsLogAboveTheTabs(t *testing.T) {
	d := cartCall(appLogAbout(uuid.Nil, launchedAt().Add(120*time.Millisecond), "Could not read GET /v2/cart", decodeError))

	for _, tab := range []string{"response", "headers"} {
		d.Phase = tab
		out := render(t, NetworkDetailPane(d))

		section := strings.Index(out, "data-linked-logs")
		tabs := strings.Index(out, "data-phase-tab")
		if section < 0 || tabs < 0 || section > tabs {
			t.Fatalf("tab %s: the section should sit between the header and the tabs (section at %d, tabs at %d)", tab, section, tabs)
		}
		if !contains(out, "Logged by the app") || !contains(out, "Could not read GET /v2/cart") {
			t.Errorf("tab %s: the app's log is not listed: %s", tab, out)
		}
		if !contains(out, "is not a subtype of type &#39;double&#39;") {
			t.Errorf("tab %s: the error text is missing: %s", tab, out)
		}
		// The tabs are the call's, unchanged by the app's log.
		if n := strings.Count(out, "data-phase-tab="); n != 2 {
			t.Errorf("tab %s: want Headers and Response, got %d tabs", tab, n)
		}
		// The call is still a complete 200: green, timed, not failed.
		header := out[:section]
		if !contains(header, ">200<") || !contains(header, "text-[#3FB950]") || !contains(header, "118ms") {
			t.Errorf("tab %s: the call no longer reads as a complete 200: %s", tab, header)
		}
		if contains(header, "text-[#F85149]") || contains(header, "pending") {
			t.Errorf("tab %s: the app's error turned the call's status red: %s", tab, header)
		}
		// The link opens every log with this id, phases included, so a count of
		// the app's logs on it would disagree with the page it opens.
		want := fmt.Sprintf(`href="/project/%s/logs?q=request%%3A%s"`, d.ProjectID, d.Selected.RequestID)
		if !contains(out, want) {
			t.Errorf("tab %s: no link to request:<id> in the log viewer, wanted %s", tab, want)
		}
		if !contains(out, ">Every log for this call</a>") {
			t.Errorf("tab %s: the link should read \"Every log for this call\" and nothing else: %s", tab, out)
		}
		// One log, listed whole: nothing is hidden, so the heading counts nothing.
		if contains(out, "data-linked-logs-count") {
			t.Errorf("tab %s: a section listing every one of its logs carried a count: %s", tab, out)
		}
	}
}

// The pane is a fixed height, and every row above the tabs is a row less of the
// body underneath, so the handlers load three and the link carries the rest.
// The heading says how many there are.
func TestTheInspectorListsThreeAppLogsAndLinksTheRest(t *testing.T) {
	d := cartCall(appLogs(LinkedLogLimit)...)
	d.LinkedTotal = 6
	out := render(t, NetworkDetailPane(d))

	for i := 1; i <= 3; i++ {
		if !contains(out, fmt.Sprintf("app log number %d", i)) {
			t.Errorf("log %d of the first three is missing", i)
		}
	}
	// The heading says rows are hidden. It counts the app's six logs, not the
	// five that were loaded: two of those are the call's phases, which are the
	// call rather than something the app logged.
	heading := between(t, out, "data-linked-logs", "</h5>")
	if !contains(heading, ">3 of 6<") {
		t.Errorf("the heading should say three of the six app logs are listed: %s", heading)
	}
	if !contains(out, ">Every log for this call</a>") || contains(out, "7 logs") {
		t.Errorf("the link carries no count, of the app's logs or of everything: %s", out)
	}
}

// The call page has the same section and the same count as the inspector. The
// count is of the app's logs: two phases and three app logs are five logs, and
// the heading still has to say three of the nine the app wrote.
func TestTheCallPageCountsTheAppLogsItLeavesOut(t *testing.T) {
	d := cartCall(appLogs(LinkedLogLimit)...)
	page := func(total int) string {
		return render(t, NetworkDetailPage(NetworkDetailData{
			ProjectID: d.ProjectID, RequestID: d.Selected.RequestID, Logs: d.Phases, LinkedTotal: total,
		}))
	}

	out := page(9)
	heading := between(t, out, "data-linked-logs", "</h2>")
	if !contains(heading, ">3 of 9<") {
		t.Errorf("the heading should say three of the app's nine logs are listed: %s", heading)
	}
	for i := 1; i <= 3; i++ {
		if !contains(out, fmt.Sprintf("app log number %d", i)) {
			t.Errorf("log %d of the three is missing", i)
		}
	}

	// Nothing left out, nothing to count.
	if heading := between(t, page(3), "data-linked-logs", "</h2>"); contains(heading, "data-linked-logs-count") {
		t.Errorf("a section listing every one of the app's logs carried a count: %s", heading)
	}
}

// The log viewer is where a decode failure is found, by searching or by level,
// so its row has to lead to the call it was about, not only a network log's.
func TestTheLogViewerLinksAnAppLogToItsCall(t *testing.T) {
	rid := uuid.New()
	l := appLogAbout(rid, launchedAt(), "Could not read GET /v2/cart", decodeError)
	l.ProjectID, l.SessionID = uuid.New(), uuid.New()

	out := render(t, LogRow(l))
	want := fmt.Sprintf(`href="/project/%s/network/%s"`, l.ProjectID, rid)
	if !contains(out, want) || !contains(out, "Request: "+rid.String()[:8]) {
		t.Errorf("an app log with a request id does not link to its call, wanted %s in %s", want, out)
	}
}

func TestACallTheAppNeverLoggedAboutHasNoSection(t *testing.T) {
	out := render(t, NetworkDetailPane(cartCall()))
	if contains(out, "data-linked-logs") || contains(out, "Logged by the app") {
		t.Errorf("a call with no app logs drew the section: %s", out)
	}
}

func TestAHostileAppLogIsRenderedAsText(t *testing.T) {
	hostile := appLogAbout(uuid.Nil, launchedAt(), `<img src=x onerror="alert(1)">`, `<script>alert(2)</script>`)
	d := cartCall(hostile)

	pages := map[string]string{
		"inspector": render(t, NetworkDetailPane(d)),
		"call page": render(t, NetworkDetailPage(NetworkDetailData{ProjectID: d.ProjectID, RequestID: d.Selected.RequestID, Logs: d.Phases})),
	}
	for name, out := range pages {
		if contains(out, "<img src=x") || contains(out, "<script>alert(2)") {
			t.Errorf("%s: an app log was rendered as markup", name)
		}
		if !contains(out, "&lt;img") || !contains(out, "&lt;script&gt;alert(2)") {
			t.Errorf("%s: the app log was not escaped into the page at all", name)
		}
	}
}

// The log viewer's Request link and the session's Inspect link both land here,
// so the app's log must not become a panel with no tab to open it.
func TestTheCallPageKeepsTheAppsLogOutOfItsTabs(t *testing.T) {
	d := cartCall(appLogAbout(uuid.Nil, launchedAt().Add(120*time.Millisecond), "Could not read GET /v2/cart", decodeError))
	out := render(t, NetworkDetailPage(NetworkDetailData{ProjectID: d.ProjectID, RequestID: d.Selected.RequestID, Logs: d.Phases}))

	if n := strings.Count(out, `data-target="tab-`); n != 2 {
		t.Errorf("want a tab for the request and the response only, got %d", n)
	}
	if n := strings.Count(out, `id="tab-`); n != 2 {
		t.Errorf("want a panel for each tab and no other, got %d", n)
	}
	if !contains(out, "data-linked-logs") || !contains(out, "Could not read GET /v2/cart") {
		t.Errorf("the app's log is not listed above the tabs: %s", out)
	}
	if strings.Index(out, "data-linked-logs") > strings.Index(out, `id="phase-tabs"`) {
		t.Error("the section should come before the tabs")
	}
	if !contains(out, "Network Request") || contains(out, "data-call-not-captured") {
		t.Errorf("a captured call read as not captured: %s", out)
	}
	want := fmt.Sprintf(`href="/project/%s/logs?q=request%%3A%s"`, d.ProjectID, d.Selected.RequestID)
	if !contains(out, want) || !contains(out, ">Every log for this call</a>") {
		t.Errorf("no \"Every log for this call\" link to request:<id>, wanted %s: %s", want, out)
	}
}

// Ordered by time, an app log stamped before the request lands first. The first
// tab then has to be the first phase, or the page opens with nothing lit.
func TestTheCallPageOpensOnItsFirstPhaseWhateverCameFirst(t *testing.T) {
	d := cartCall()
	early := appLogAbout(d.Selected.RequestID, launchedAt().Add(-time.Second), "logged before the request", "")
	logs := append([]domain.Log{early}, d.Phases...)
	out := render(t, NetworkDetailPage(NetworkDetailData{ProjectID: d.ProjectID, RequestID: d.Selected.RequestID, Logs: logs}))

	first := strings.Index(out, `data-target="tab-0"`)
	if first < 0 {
		t.Fatalf("no first tab: %s", out)
	}
	button := out[strings.LastIndex(out[:first], "<button"):first]
	if !contains(button, "border-b-cs-primary") {
		t.Errorf("the first tab is not lit: %s", button)
	}
	if panel := between(t, out, `id="tab-0"`, ">"); contains(panel, "hidden") {
		t.Errorf("the first panel starts hidden: %s", panel)
	}
	if !contains(between(t, out, `id="tab-0"`, `id="tab-1"`), "Network request") {
		t.Error("the first panel is not the request phase")
	}
}

// between is s from the first from up to the next to, failing the test rather
// than panicking when either is missing, so one broken page does not stop the
// rest of the package from reporting.
func between(t *testing.T, s, from, to string) string {
	t.Helper()
	i := strings.Index(s, from)
	if i < 0 {
		t.Fatalf("%q is missing from %s", from, s)
	}
	j := strings.Index(s[i:], to)
	if j < 0 {
		t.Fatalf("%q is missing after %q in %s", to, from, s)
	}
	return s[i : i+j]
}

// Phases dropped at the SDK's level gate or by retention leave the app's log as
// the only one with the id, which is not an empty "Network Request".
func TestAnUncapturedCallSaysSoInPlainWords(t *testing.T) {
	rid := uuid.New()
	out := render(t, NetworkDetailPage(NetworkDetailData{
		ProjectID: uuid.New(), RequestID: rid,
		Logs: []domain.Log{appLogAbout(rid, launchedAt(), "Could not read GET /v2/cart", decodeError)},
	}))

	if !contains(out, "Call not captured") || !contains(out, "data-call-not-captured") {
		t.Errorf("an uncaptured call did not say so: %s", out)
	}
	if !contains(out, "request "+rid.String()) {
		t.Errorf("the page does not name the request id it was asked about: %s", out)
	}
	if !contains(out, "The SDK writes network logs at debug") {
		t.Errorf("the page does not say why: %s", out)
	}
	if !contains(out, "Could not read GET /v2/cart") {
		t.Errorf("the app's log is missing: %s", out)
	}
	if contains(out, "Network Request") || contains(out, `id="phase-tabs"`) || contains(out, "Request not found") {
		t.Errorf("an uncaptured call drew an empty call: %s", out)
	}
}

func TestAnUnknownRequestIsStillNotFound(t *testing.T) {
	out := render(t, NetworkDetailPage(NetworkDetailData{ProjectID: uuid.New(), RequestID: uuid.New()}))
	if !contains(out, "Request not found") || contains(out, "Call not captured") || contains(out, "data-linked-logs") {
		t.Errorf("an id with no logs at all should be not found: %s", out)
	}
}

// 12.0 is a double to the app's model and 12 an int, and an id past 2^53 does
// not survive a float64. The body is the evidence for a decode failure, so it
// has to read exactly as the app received it.
func TestNumbersRenderAsTheAppSentThem(t *testing.T) {
	const body = `{"status_code":200,"body":{"price":12.0,"id":9007199254740993,"n":12}}`
	rid := uuid.New()
	d := cartCall()
	d.Phases[1] = netLog(d.Selected.RequestID, "response", launchedAt().Add(118*time.Millisecond), body)

	meta := json.RawMessage(body)
	logRow := domain.Log{ID: uuid.New(), SessionID: uuid.New(), Level: "debug", Message: "Network response", Metadata: &meta, RequestID: &rid}

	pages := map[string]string{
		"inspector":  render(t, NetworkDetailPane(d)),
		"log viewer": render(t, LogRow(logRow)),
		"call page":  render(t, NetworkDetailPage(NetworkDetailData{ProjectID: d.ProjectID, RequestID: d.Selected.RequestID, Logs: d.Phases})),
	}
	for name, out := range pages {
		for _, want := range []string{"price&#34;: 12.0", "id&#34;: 9007199254740993,", "n&#34;: 12,"} {
			if !contains(out, want) {
				t.Errorf("%s: wanted %q in %s", name, want, out)
			}
		}
		if contains(out, "9007199254740992") {
			t.Errorf("%s: the id was rounded through a float64", name)
		}
	}
}

// openingTag is the tag that carries marker, from its "<" to its ">".
func openingTag(t *testing.T, s, marker string) string {
	t.Helper()
	at := strings.Index(s, marker)
	if at < 0 {
		t.Fatalf("%q is missing from %s", marker, s)
	}
	start := strings.LastIndex(s[:at], "<")
	end := strings.Index(s[at:], ">")
	if start < 0 || end < 0 {
		t.Fatalf("%q is not inside a tag in %s", marker, s)
	}
	return s[start : at+end+1]
}

// An app at the default minimumLevel stores no network logs, so its Network
// screen has no calls at all, and any call it is asked about was not captured.
// The screen's empty state says to add an interceptor, which the request id
// proves is there, so it cannot stand in for the pane.
func TestTheNetworkScreenWithNoCallsSaysACallWasNotCaptured(t *testing.T) {
	rid := uuid.New()
	d := NetworkData{
		ProjectID: uuid.New(), Uncaptured: &rid, LinkedTotal: 1,
		Phases: []domain.Log{appLogAbout(rid, launchedAt(), "Could not read GET /v2/cart", decodeError)},
	}
	out := render(t, NetworkPage(d))

	for _, want := range []string{`id="network-detail"`, "data-call-not-captured", "call not captured", "Could not read GET /v2/cart", "request " + rid.String()} {
		if !contains(out, want) {
			t.Errorf("the pane is missing %q: %s", want, out)
		}
	}
	if contains(out, "No network calls yet") || contains(out, "Add the Dio interceptor") {
		t.Errorf("the empty state stood in for the pane: %s", out)
	}
	if !contains(out, ">No network calls were stored.</p>") {
		t.Errorf("the empty list beside the pane does not say it is empty: %s", out)
	}
	// No list to swap back to, so closing loads the screen without the call.
	if tag := openingTag(t, out, "data-dismiss-inspector"); !strings.HasPrefix(tag, "<a ") || contains(tag, "hx-get") ||
		!contains(tag, fmt.Sprintf(`href="/project/%s/network?rid="`, d.ProjectID)) {
		t.Errorf("closing the pane should be a link to the screen with nothing selected: %s", tag)
	}

	d.Filter = domain.NetworkFilter{Path: "cart"}
	if out := render(t, NetworkPage(d)); !contains(out, ">Nothing matches that filter.</p>") || !contains(out, "data-call-not-captured") {
		t.Errorf("a filtered empty list should say the filter matched nothing, beside the pane: %s", out)
	}

	// With a call listed the pane is closed in place, as for any selection.
	d.Filter = domain.NetworkFilter{}
	d.Calls = []domain.NetworkCall{{RequestID: uuid.New(), StartedAt: launchedAt(), HasRequest: true}}
	out = render(t, NetworkPage(d))
	if tag := openingTag(t, out, "data-dismiss-inspector"); !strings.HasPrefix(tag, "<button") || !contains(tag, "hx-get=") {
		t.Errorf("beside a list, closing the pane should swap it in place: %s", tag)
	}
	if contains(out, "data-network-none") {
		t.Errorf("a list with a call in it said it was empty: %s", out)
	}

	// No calls and no call asked about is still the empty state.
	empty := NetworkData{ProjectID: d.ProjectID}
	if out := render(t, NetworkPage(empty)); !contains(out, "No network calls yet") || contains(out, `id="network-detail"`) {
		t.Errorf("with nothing to show, the screen should be its empty state: %s", out)
	}
}

// The launch's Network tab is the same split, and "this launch made no network
// calls" beside a call it made would be false: the call was made and not
// stored.
func TestALaunchWithNoStoredCallsSaysACallWasNotCaptured(t *testing.T) {
	rid := uuid.New()
	d := detailFor("network", nil, nil)
	d.Network.Uncaptured = &rid
	d.Network.Phases = []domain.Log{appLogAbout(rid, launchedAt().Add(time.Second), "Could not read GET /v2/cart", decodeError)}
	d.Network.LinkedTotal = 1
	out := render(t, SessionDetailPage(d))

	for _, want := range []string{`id="network-detail"`, "data-call-not-captured", "call not captured", "Could not read GET /v2/cart", "request " + rid.String()} {
		if !contains(out, want) {
			t.Errorf("the pane is missing %q: %s", want, out)
		}
	}
	if contains(out, "made no network calls") {
		t.Errorf("the launch's empty state stood in for the pane: %s", out)
	}
	if !contains(out, ">No network calls were stored for this launch.</p>") {
		t.Errorf("the empty list beside the pane does not say it is empty: %s", out)
	}
	want := fmt.Sprintf(`href="/project/%s/session/%s?rid=&amp;tab=network"`, d.ProjectID, d.SessionID)
	if tag := openingTag(t, out, "data-dismiss-inspector"); !strings.HasPrefix(tag, "<a ") || contains(tag, "hx-get") || !contains(tag, want) {
		t.Errorf("closing the pane should be a link to the launch's tab with nothing selected, wanted %s: %s", want, tag)
	}
}
