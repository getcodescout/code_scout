package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getcodescout/code_scout/internal/domain"
	"github.com/getcodescout/code_scout/internal/ports"
	"github.com/getcodescout/code_scout/internal/services"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type inspectorLogs struct {
	ports.LogRepository
	calls     []domain.NetworkCall
	byRequest map[uuid.UUID][]domain.Log
}

func (f *inspectorLogs) ListNetworkCalls(context.Context, uuid.UUID, domain.NetworkFilter, int) ([]domain.NetworkCall, error) {
	return f.calls, nil
}

// GetByRequestID keeps the repository's contract: every phase, the earliest
// linkedLimit of the app's own logs, and a count of all of them.
func (f *inspectorLogs) GetByRequestID(_ context.Context, _, requestID uuid.UUID, linkedLimit int) ([]domain.Log, int, error) {
	var logs []domain.Log
	linked := 0
	for _, l := range f.byRequest[requestID] {
		if !l.IsNetworkCall {
			if linked++; linked > linkedLimit {
				continue
			}
		}
		logs = append(logs, l)
	}
	return logs, linked, nil
}

func inspectRequest(t *testing.T, logs *inspectorLogs, projectID, requestID uuid.UUID) string {
	t.Helper()
	h := NewLogViewerHandler(services.NewLogQueryService(logs, nil), nil, nil, nil)
	r := httptest.NewRequest("GET", "/project/"+projectID.String()+"/network/inspector?rid="+requestID.String(), nil)
	r = mux.SetURLVars(r, map[string]string{"id": projectID.String()})
	w := httptest.NewRecorder()
	h.NetworkInspector(w, r)
	if w.Code != 200 {
		t.Fatalf("inspector answered %d: %s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

// An rid with no row in the list is one of three things, and only one of them
// is a call nobody stored: the app logged against it and no phase exists.
func TestTheInspectorSaysWhenACallWasNotCaptured(t *testing.T) {
	projectID := uuid.New()
	listed, filteredOut, uncaptured, unknown := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	at := time.Date(2026, 9, 23, 9, 14, 3, 0, time.UTC)
	errText := "type 'int' is not a subtype of type 'double' in type cast"
	request := domain.CallPhaseRequest

	logs := &inspectorLogs{
		calls: []domain.NetworkCall{{RequestID: listed, StartedAt: at, HasRequest: true}},
		byRequest: map[uuid.UUID][]domain.Log{
			filteredOut: {{Message: "Network request", IsNetworkCall: true, RequestID: &filteredOut, CallPhase: &request, TimeStamp: at}},
			uncaptured:  {{Level: "error", Message: "Could not read GET /v2/cart", Error: &errText, RequestID: &uncaptured, TimeStamp: at}},
		},
	}

	out := inspectRequest(t, logs, projectID, uncaptured)
	for _, want := range []string{"call not captured", "data-call-not-captured", "The SDK writes network logs at debug", "Could not read GET /v2/cart", "request " + uncaptured.String()} {
		if !strings.Contains(out, want) {
			t.Errorf("the uncaptured call's pane is missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "Select a call to inspect it") || strings.Contains(out, "data-phase-tab") {
		t.Errorf("an uncaptured call drew an empty call or tabs: %s", out)
	}

	for name, rid := range map[string]uuid.UUID{"a call the filter left out": filteredOut, "an id with no logs": unknown} {
		out := inspectRequest(t, logs, projectID, rid)
		if !strings.Contains(out, "Select a call to inspect it") || strings.Contains(out, "call not captured") {
			t.Errorf("%s should select nothing, got: %s", name, out)
		}
	}
}

// noProjects answers the sidebar's project lookup with nothing, which the call
// page draws as a shell without a name.
type noProjects struct{ ports.ProjectManager }

func (noProjects) GetProject(context.Context, uuid.UUID) (*domain.Project, int, error) {
	return nil, 404, domain.ErrNotFound
}

func callPage(t *testing.T, logs *inspectorLogs, projectID, requestID uuid.UUID) string {
	t.Helper()
	h := NewLogViewerHandler(services.NewLogQueryService(logs, nil), noProjects{}, nil, nil)
	r := httptest.NewRequest("GET", "/project/"+projectID.String()+"/network/"+requestID.String(), nil)
	r = mux.SetURLVars(r, map[string]string{"id": projectID.String(), "rid": requestID.String()})
	w := httptest.NewRecorder()
	h.NetworkDetail(w, r)
	if w.Code != 200 {
		t.Fatalf("call page answered %d: %s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

// The app can put one call's request id on as many logs as it likes, one for
// each list item its model rejected. Opening the call loads three of them and
// counts the rest: on the inspector and on the call page, and for a call whose
// phases were never stored as well as for one that was. Six, because the two
// phases and three app logs that are loaded make five.
func TestOpeningACallLoadsThreeOfTheAppsLogsAndCountsTheRest(t *testing.T) {
	projectID, cart, uncaptured := uuid.New(), uuid.New(), uuid.New()
	at := time.Date(2026, 9, 23, 9, 14, 3, 0, time.UTC)
	request, response := domain.CallPhaseRequest, domain.CallPhaseResponse
	appLogs := func(rid uuid.UUID) []domain.Log {
		var logs []domain.Log
		for i := 1; i <= 6; i++ {
			logs = append(logs, domain.Log{
				Level: "error", Message: fmt.Sprintf("Could not read item %d", i),
				RequestID: &rid, TimeStamp: at.Add(time.Duration(i) * time.Second),
			})
		}
		return logs
	}
	logs := &inspectorLogs{
		calls: []domain.NetworkCall{{RequestID: cart, StartedAt: at, HasRequest: true, HasResponse: true}},
		byRequest: map[uuid.UUID][]domain.Log{
			cart: append([]domain.Log{
				{Message: "Network request", IsNetworkCall: true, RequestID: &cart, CallPhase: &request, TimeStamp: at},
				{Message: "Network response", IsNetworkCall: true, RequestID: &cart, CallPhase: &response, TimeStamp: at.Add(118 * time.Millisecond)},
			}, appLogs(cart)...),
			uncaptured: appLogs(uncaptured),
		},
	}

	for name, out := range map[string]string{
		"inspector":                    inspectRequest(t, logs, projectID, cart),
		"call page":                    callPage(t, logs, projectID, cart),
		"inspector, call not captured": inspectRequest(t, logs, projectID, uncaptured),
		"call page, call not captured": callPage(t, logs, projectID, uncaptured),
	} {
		for i := 1; i <= 3; i++ {
			if !strings.Contains(out, fmt.Sprintf("Could not read item %d", i)) {
				t.Errorf("%s: the app's log %d is missing", name, i)
			}
		}
		for i := 4; i <= 6; i++ {
			if strings.Contains(out, fmt.Sprintf("Could not read item %d", i)) {
				t.Errorf("%s: loaded the app's log %d, past the three it lists", name, i)
			}
		}
		if !strings.Contains(out, ">3 of 6<") {
			t.Errorf("%s: the heading should say three of the app's six logs are listed: %s", name, out)
		}
	}
}

// GetBySessionID is the launch's Logs tab: every log stored with its id.
func (f *inspectorLogs) GetBySessionID(_ context.Context, _, sessionID uuid.UUID, _ int) ([]domain.Log, error) {
	var logs []domain.Log
	for _, byRequest := range f.byRequest {
		for _, l := range byRequest {
			if l.SessionID == sessionID {
				logs = append(logs, l)
			}
		}
	}
	return logs, nil
}

// noSessionRows is a launch whose session row never arrived, which the
// session screen renders from its logs.
type noSessionRows struct{ ports.SessionRepository }

func (noSessionRows) GetByID(context.Context, uuid.UUID, uuid.UUID) (*domain.Session, error) {
	return nil, domain.ErrNotFound
}

func networkScreen(t *testing.T, logs *inspectorLogs, projectID, requestID uuid.UUID) string {
	t.Helper()
	h := NewLogViewerHandler(services.NewLogQueryService(logs, nil), noProjects{}, nil, nil)
	r := httptest.NewRequest("GET", "/project/"+projectID.String()+"/network?rid="+requestID.String(), nil)
	r = mux.SetURLVars(r, map[string]string{"id": projectID.String()})
	w := httptest.NewRecorder()
	h.Network(w, r)
	if w.Code != 200 {
		t.Fatalf("network screen answered %d: %s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

func launchNetworkTab(t *testing.T, logs *inspectorLogs, projectID, sessionID, requestID uuid.UUID) string {
	t.Helper()
	h := NewLogViewerHandler(services.NewLogQueryService(logs, noSessionRows{}), noProjects{}, nil, nil)
	r := httptest.NewRequest("GET", "/project/"+projectID.String()+"/session/"+sessionID.String()+"?tab=network&rid="+requestID.String(), nil)
	r = mux.SetURLVars(r, map[string]string{"id": projectID.String(), "sid": sessionID.String()})
	w := httptest.NewRecorder()
	h.SessionTimeline(w, r)
	if w.Code != 200 {
		t.Fatalf("session screen answered %d: %s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

// An app at the default minimumLevel stores no network logs, so a link to one
// of its calls opens a list with nothing in it. Both screens still show the
// pane the handler prepared, and a launch shows it only for a call the app
// logged about in that launch.
func TestAScreenWithNoCallsStillSaysACallWasNotCaptured(t *testing.T) {
	projectID, launch, otherLaunch, uncaptured := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	at := time.Date(2026, 9, 23, 9, 14, 3, 0, time.UTC)
	errText := "type 'int' is not a subtype of type 'double' in type cast"
	logs := &inspectorLogs{byRequest: map[uuid.UUID][]domain.Log{
		uncaptured: {{Level: "error", Message: "Could not read GET /v2/cart", Error: &errText, RequestID: &uncaptured, SessionID: launch, TimeStamp: at}},
	}}

	for name, out := range map[string]string{
		"the Network screen":       networkScreen(t, logs, projectID, uncaptured),
		"the launch's Network tab": launchNetworkTab(t, logs, projectID, launch, uncaptured),
	} {
		for _, want := range []string{"data-call-not-captured", "call not captured", "Could not read GET /v2/cart", "request " + uncaptured.String()} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: the pane is missing %q: %s", name, want, out)
			}
		}
		for _, emptyState := range []string{"No network calls yet", "made no network calls"} {
			if strings.Contains(out, emptyState) {
				t.Errorf("%s: the empty state %q stood in for the pane: %s", name, emptyState, out)
			}
		}
	}

	out := launchNetworkTab(t, logs, projectID, otherLaunch, uncaptured)
	if !strings.Contains(out, "This launch made no network calls.") || strings.Contains(out, "call not captured") ||
		strings.Contains(out, "Could not read GET /v2/cart") {
		t.Errorf("another launch's call drew its pane on this launch's tab: %s", out)
	}
}
