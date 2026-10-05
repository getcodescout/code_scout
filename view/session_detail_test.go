package view

import (
	"strings"
	"testing"
	"time"

	"github.com/getcodescout/code_scout/internal/domain"
	"github.com/google/uuid"
)

func launchedAt() time.Time {
	return time.Date(2026, 8, 5, 14, 28, 2, 0, time.UTC)
}

func detailFor(tab string, logs []domain.Log, calls []domain.NetworkCall) SessionDetailData {
	start := launchedAt()
	projectID, sessionID := uuid.New(), uuid.New()

	d := SessionDetailData{
		User:      &domain.User{Name: "T", Email: "t@test.local"},
		ProjectID: projectID,
		Project:   &domain.Project{},
		SessionID: sessionID,
		Session: &domain.Session{
			StartedAt:  start,
			LastSeenAt: start.Add(93 * time.Second),
		},
		Tab:  tab,
		Logs: logs,
		Network: NetworkData{
			ProjectID:    projectID,
			Filter:       domain.NetworkFilter{SessionID: &sessionID},
			Calls:        calls,
			SessionStart: start,
		},
	}
	if len(calls) > 0 {
		d.Network.Selected = &d.Network.Calls[0]
	}
	return d
}

func TestOffsetLabelReadsAsTimeSinceLaunch(t *testing.T) {
	start := launchedAt()
	cases := []struct {
		at   time.Time
		want string
	}{
		{start, "+0.000s"},
		{start.Add(1200 * time.Millisecond), "+1.200s"},
		{start.Add(59*time.Second + 999*time.Millisecond), "+59.999s"},
		// Past a minute it switches to m:ss, because nobody reads "83.472s"
		// as "a minute and a bit".
		{start.Add(83 * time.Second), "+1:23"},
		{start.Add(10 * time.Minute), "+10:00"},
	}
	for _, c := range cases {
		if got := offsetLabel(c.at, start); got != c.want {
			t.Errorf("offsetLabel(+%v) = %q, want %q", c.at.Sub(start), got, c.want)
		}
	}
}

func TestOffsetLabelStaysBlankRatherThanPrintingNonsense(t *testing.T) {
	start := launchedAt()
	// A log stamped before its own session row. Possible when a device's clock
	// moved, and "-4.000s since launch" is worse than nothing at all.
	if got := offsetLabel(start.Add(-4*time.Second), start); got != "" {
		t.Errorf("a negative offset rendered as %q", got)
	}
	if got := offsetLabel(start, time.Time{}); got != "" {
		t.Errorf("an unknown launch time rendered as %q", got)
	}
	if got := offsetLabel(time.Time{}, start); got != "" {
		t.Errorf("an unknown event time rendered as %q", got)
	}
}

func TestSessionSpanIsTheLaunchLength(t *testing.T) {
	start := launchedAt()
	span := func(d time.Duration) string {
		return sessionSpan(&domain.Session{StartedAt: start, LastSeenAt: start.Add(d)})
	}
	if got := span(45 * time.Second); got != "45s" {
		t.Errorf("got %q", got)
	}
	if got := span(93 * time.Second); got != "1m 33s" {
		t.Errorf("got %q", got)
	}
	if got := span(2*time.Hour + 5*time.Minute); got != "2h 05m" {
		t.Errorf("got %q", got)
	}
	if got := sessionSpan(nil); got != "" {
		t.Errorf("a missing session rendered a span of %q", got)
	}
}

// The offset is measured from the launch, not from the first log. An app that
// sat idle for ten seconds before logging anything would otherwise report its
// first event at +0.000s and hide exactly the gap worth seeing.
func TestOffsetsAreMeasuredFromTheLaunchNotTheFirstLog(t *testing.T) {
	start := launchedAt()
	d := detailFor("logs", []domain.Log{
		{Level: "info", Message: "first log", TimeStamp: start.Add(10 * time.Second)},
	}, nil)

	if got := d.startedAt(); !got.Equal(start) {
		t.Fatalf("startedAt = %v, want the session's own start %v", got, start)
	}
	html := render(t, SessionDetailPage(d))
	if !strings.Contains(html, "+10.000s") {
		t.Error("the first log did not report its distance from launch")
	}
}

// A launch whose session row never arrived still has to render: an SDK older
// than 1.2.0 uploaded logs without sending one.
func TestASessionWithNoRowStillRenders(t *testing.T) {
	start := launchedAt()
	d := detailFor("logs", []domain.Log{
		{Level: "info", Message: "orphaned log", TimeStamp: start},
		{Level: "error", Message: "and another", TimeStamp: start.Add(2 * time.Second)},
	}, nil)
	d.Session = nil

	html := render(t, SessionDetailPage(d))
	if !strings.Contains(html, "orphaned log") {
		t.Error("the logs did not render without a session row")
	}
	// Offsets fall back to the first event, so the column still means something.
	if !strings.Contains(html, "+2.000s") {
		t.Error("offsets did not fall back to the first event")
	}
}

func TestBothTabsCarryTheirCountsWhicheverIsShowing(t *testing.T) {
	start := launchedAt()
	logs := []domain.Log{
		{Level: "info", Message: "a", TimeStamp: start},
		{Level: "info", Message: "b", TimeStamp: start.Add(time.Second)},
	}
	calls := []domain.NetworkCall{
		{RequestID: uuid.New(), StartedAt: start, EndedAt: start.Add(120 * time.Millisecond),
			HasRequest: true, HasResponse: true},
	}

	// A count you cannot see until you click the tab is not a count, so both
	// panes are loaded whichever one is rendered.
	for _, tab := range []string{"logs", "network"} {
		html := render(t, SessionDetailPage(detailFor(tab, logs, calls)))
		if !strings.Contains(html, "Logs (2)") {
			t.Errorf("tab %q: the logs count is missing", tab)
		}
		if !strings.Contains(html, "Network (1)") {
			t.Errorf("tab %q: the network count is missing", tab)
		}
	}
}

func TestTheTabIsInTheURLSoItCanBeShared(t *testing.T) {
	d := detailFor("network", nil, nil)
	html := render(t, SessionDetailPage(d))

	if !strings.Contains(html, "?tab=network") || !strings.Contains(html, "?tab=logs") {
		t.Error("the tabs are not links carrying their state in the query string")
	}
}

func TestTheNetworkTabShowsStatusAndDurationTheJourneyCouldNot(t *testing.T) {
	start := launchedAt()
	status := 402
	method := "POST"
	url := "https://api.shop.dev/v2/payments/confirm"

	d := detailFor("network", nil, []domain.NetworkCall{{
		RequestID:   uuid.New(),
		Method:      &method,
		URL:         &url,
		StatusCode:  &status,
		StartedAt:   start.Add(3 * time.Second),
		EndedAt:     start.Add(3*time.Second + 842*time.Millisecond),
		HasRequest:  true,
		HasResponse: true,
	}})

	html := render(t, SessionDetailPage(d))
	for _, want := range []string{"POST", "/v2/payments/confirm", "402", "842", "+3.000s"} {
		if !strings.Contains(html, want) {
			t.Errorf("the network row is missing %q", want)
		}
	}
}

// The Network tab is the Network screen's split pane scoped to one launch.
// Clicking a row used to leave for a page of its own, so reading the second
// call in a launch meant going back and finding your place again.
func TestTheNetworkTabInspectsACallWithoutLeavingTheLaunch(t *testing.T) {
	start := launchedAt()
	requestID := uuid.New()
	method := "POST"
	d := detailFor("network", nil, []domain.NetworkCall{{
		RequestID:  requestID,
		Method:     &method,
		StartedAt:  start.Add(3 * time.Second),
		EndedAt:    start.Add(3*time.Second + 120*time.Millisecond),
		HasRequest: true, HasResponse: true,
	}})

	html := render(t, SessionDetailPage(d))

	// The pane every row targets. htmx fails silently on a missing target, so
	// without it the click would just do nothing.
	if !strings.Contains(html, `id="network-detail"`) {
		t.Error("the launch's network tab has no inspector pane")
	}
	if !strings.Contains(html, `hx-target="#network-detail"`) {
		t.Error("the rows do not swap the pane")
	}
	// And the row goes to the fragment endpoint rather than navigating.
	if !strings.Contains(html, "/network/inspector?") {
		t.Error("a row still navigates instead of swapping the pane")
	}
	if strings.Contains(html, "window.location.assign") {
		t.Error("a row still leaves the launch on click")
	}
	// The address it pushes is the launch, not the Network screen.
	pushed := `hx-push-url="/project/` + d.ProjectID.String() + "/session/" + d.SessionID.String() +
		"?rid=" + requestID.String() + "&amp;tab=network\""
	if !strings.Contains(html, pushed) {
		t.Errorf("the pushed address left the launch, wanted %s in %s", pushed, html)
	}
}

// A log the app wrote about a call carries the call's request id and is not a
// network log. It is the row you are reading when a body broke the app, so it
// links to the call like the phases do.
func TestAnAppLogWithARequestIDLinksToItsCall(t *testing.T) {
	start := launchedAt()
	rid := uuid.New()
	errText := "type 'int' is not a subtype of type 'double' in type cast"
	d := detailFor("logs", []domain.Log{
		{Level: "info", Message: "user opened the cart", TimeStamp: start},
		{Level: "error", Message: "Could not read GET /v2/cart", Error: &errText, RequestID: &rid, TimeStamp: start.Add(time.Second)},
	}, nil)

	html := render(t, SessionDetailPage(d))
	if n := strings.Count(html, "Inspect this call"); n != 1 {
		t.Errorf("want one link, on the app log only, got %d", n)
	}
	href := `href="/project/` + d.ProjectID.String() + "/network/" + rid.String() + `"`
	if !strings.Contains(html, href) {
		t.Errorf("the app log does not link to its call, wanted %s", href)
	}
}

func TestAHostileLogMessageIsRenderedAsText(t *testing.T) {
	start := launchedAt()
	d := detailFor("logs", []domain.Log{
		{Level: "info", Message: `<img src=x onerror="alert(1)">`, TimeStamp: start},
	}, nil)

	html := render(t, SessionDetailPage(d))
	if strings.Contains(html, "<img src=x") {
		t.Error("a log message was rendered as markup")
	}
	if !strings.Contains(html, "&lt;img") {
		t.Error("the message was not escaped into the page at all")
	}
}

func TestEmptyPanesSayWhichThingIsMissing(t *testing.T) {
	logs := render(t, SessionDetailPage(detailFor("logs", nil, nil)))
	if !strings.Contains(logs, "Nothing was logged") {
		t.Error("an empty logs pane did not say so")
	}
	network := render(t, SessionDetailPage(detailFor("network", nil, nil)))
	if !strings.Contains(network, "made no network calls") {
		t.Error("an empty network pane did not say so")
	}
}
