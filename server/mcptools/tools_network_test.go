package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getcodescout/code_scout/internal/domain"
	"github.com/google/uuid"
)

// A body whose numbers only mean something as written: 12.0 is a double to the
// app's model and 12 an int, and 9007199254740993 does not survive a float64.
const exactBody = `{"body":{"price":12.0,"id":9007199254740993,"n":12}}`

func assertExactNumbers(t *testing.T, tool, text string) {
	t.Helper()
	for _, want := range []string{`"price":12.0`, `"id":9007199254740993`, `"n":12`} {
		if !strings.Contains(text, want) {
			t.Errorf("%s changed a number, wanted %s in %s", tool, want, text)
		}
	}
	if strings.Contains(text, "9007199254740992") {
		t.Errorf("%s rounded the id through a float64: %s", tool, text)
	}
}

func TestNumbersReachTheAgentAsTheAppSentThem(t *testing.T) {
	projectID, logID, requestID := uuid.New(), uuid.New(), uuid.New()
	meta := json.RawMessage(exactBody)
	response := domain.CallPhaseResponse
	stored := domain.Log{
		ID: logID, SessionID: uuid.New(), Level: "debug", Message: "Network response",
		IsNetworkCall: true, RequestID: &requestID, CallPhase: &response, Metadata: &meta,
	}
	logs := &fakeLogs{
		listLogs: func(string, *domain.LogCursor, int) (*domain.LogListResult, error) {
			return &domain.LogListResult{Items: []domain.Log{stored}}, nil
		},
		getLog:    func(uuid.UUID) (*domain.Log, error) { return &stored, nil },
		byRequest: func(uuid.UUID) ([]domain.Log, error) { return []domain.Log{stored}, nil },
	}
	cs := session(t, Deps{Logs: logs, Access: fakeAccess{readable: map[uuid.UUID]bool{projectID: true}}})

	for tool, args := range map[string]map[string]any{
		"get_log":             {"project_id": projectID.String(), "log_id": logID.String()},
		"get_network_request": {"project_id": projectID.String(), "request_id": requestID.String()},
		"search_logs":         {"project_id": projectID.String()},
	} {
		res := call(t, cs, tool, args)
		if res.IsError {
			t.Fatalf("%s failed: %s", tool, resultText(res))
		}
		assertExactNumbers(t, tool, resultText(res))
	}
}

// A result carries the output twice, as structuredContent and as text, and an
// agent may read either. The client the tests above use decodes
// structuredContent into `any` before it can be looked at, so this reads the
// bytes the production handler writes.
func TestStructuredContentCarriesNumbersAsTheAppSentThem(t *testing.T) {
	projectID, logID := uuid.New(), uuid.New()
	meta := json.RawMessage(exactBody)
	stored := domain.Log{ID: logID, SessionID: uuid.New(), Level: "debug", Message: "Network response", Metadata: &meta}
	logs := &fakeLogs{getLog: func(uuid.UUID) (*domain.Log, error) { return &stored, nil }}
	ts := httptest.NewServer(NewHTTPHandler(Deps{
		Logs: logs, Access: fakeAccess{readable: map[uuid.UUID]bool{projectID: true}},
		Projects: fakeProjects{}, Settings: fakeSettings{},
	}))
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_log","arguments":{"project_id":%q,"log_id":%q}}}`,
		projectID, logID)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var envelope struct {
		Result struct {
			IsError           bool            `json:"isError"`
			StructuredContent json.RawMessage `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode the response: %v: %s", err, body)
	}
	if envelope.Result.IsError || len(envelope.Result.StructuredContent) == 0 {
		t.Fatalf("get_log returned no structured content: %s", body)
	}
	assertExactNumbers(t, "get_log's structuredContent", string(envelope.Result.StructuredContent))
}

// The shape an agent reads back, parsed rather than searched, so a log landing
// in the wrong list fails even when its text is present.
type networkRequestResult struct {
	Phases []struct {
		Message   string  `json:"message"`
		CallPhase *string `json:"call_phase"`
	} `json:"phases"`
	LinkedLogs []struct {
		Message   string  `json:"message"`
		Error     *string `json:"error"`
		CallPhase *string `json:"call_phase"`
	} `json:"linked_logs"`
	LinkedLogsTotal  int    `json:"linked_logs_total"`
	LinkedLogsCapped bool   `json:"linked_logs_capped"`
	Note             string `json:"note"`
}

func appLog(requestID uuid.UUID, at time.Time) domain.Log {
	msg := "type 'int' is not a subtype of type 'double' in type cast"
	return domain.Log{
		ID: uuid.New(), SessionID: uuid.New(), Level: "error",
		Message: "Could not read GET /v2/cart", Error: &msg,
		RequestID: &requestID, TimeStamp: at,
	}
}

func networkLog(requestID uuid.UUID, phase domain.CallPhase, at time.Time) domain.Log {
	return domain.Log{
		ID: uuid.New(), SessionID: uuid.New(), Level: "debug", Message: "Network " + string(phase),
		IsNetworkCall: true, RequestID: &requestID, CallPhase: &phase, TimeStamp: at,
	}
}

// A decode failure the app logged against a call is not the call's error
// phase. Presented as one, an agent reads a 200 as a failed request.
func TestGetNetworkRequestKeepsTheAppsLogsOutOfThePhases(t *testing.T) {
	projectID, requestID := uuid.New(), uuid.New()
	at := time.Now()
	logs := &fakeLogs{byRequest: func(uuid.UUID) ([]domain.Log, error) {
		return []domain.Log{
			networkLog(requestID, domain.CallPhaseRequest, at),
			networkLog(requestID, domain.CallPhaseResponse, at.Add(118*time.Millisecond)),
			appLog(requestID, at.Add(120*time.Millisecond)),
		}, nil
	}}
	cs := session(t, Deps{Logs: logs, Access: fakeAccess{readable: map[uuid.UUID]bool{projectID: true}}})

	res := call(t, cs, "get_network_request", map[string]any{"project_id": projectID.String(), "request_id": requestID.String()})
	if res.IsError {
		t.Fatalf("get_network_request failed: %s", resultText(res))
	}
	var out networkRequestResult
	if err := json.Unmarshal([]byte(resultText(res)), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(out.Phases) != 2 {
		t.Fatalf("want the request and the response as phases, got %+v", out.Phases)
	}
	for _, p := range out.Phases {
		if p.CallPhase == nil {
			t.Errorf("a log with no phase was listed as a phase: %+v", p)
		}
	}
	if len(out.LinkedLogs) != 1 || out.LinkedLogs[0].Message != "Could not read GET /v2/cart" {
		t.Fatalf("want the app's log under linked_logs, got %+v", out.LinkedLogs)
	}
	if e := out.LinkedLogs[0].Error; e == nil || !strings.Contains(*e, "is not a subtype of type 'double'") {
		t.Errorf("the linked log lost its error text: %v", e)
	}
	if out.LinkedLogsTotal != 1 || out.LinkedLogsCapped {
		t.Errorf("want one linked log counted and nothing left out, got %d, capped %v", out.LinkedLogsTotal, out.LinkedLogsCapped)
	}
	if out.Note != "" {
		t.Errorf("a captured call carries no note, got %q", out.Note)
	}

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	for _, tool := range tools.Tools {
		switch tool.Name {
		case "get_network_request":
			if !strings.Contains(tool.Description, "linked_logs") {
				t.Errorf("the description does not tell an agent what linked_logs are: %q", tool.Description)
			}
			if schema, _ := json.Marshal(tool.OutputSchema); !strings.Contains(string(schema), `"linked_logs"`) {
				t.Errorf("the output schema does not list linked_logs: %s", schema)
			}
		case "search_logs":
			// request:UUID answers with the app's own logs as well as the
			// phases, and an agent told only "one network call" would read
			// the app's error as the call's.
			_, after, found := strings.Cut(tool.Description, "request:UUID")
			clause, _, _ := strings.Cut(after, " | ")
			if !found || !strings.Contains(clause, "phases") || !strings.Contains(clause, "linked") {
				t.Errorf("the request:UUID syntax does not say it returns the app's linked logs too: %q", clause)
			}
		}
	}
}

// Phases dropped at the SDK's level gate, or by retention, leave the app's log
// as the only one with the id. That is a call nobody stored, not a call that
// does not exist, and an agent told "not found" stops looking at the log.
func TestAnUncapturedCallIsNotANotFound(t *testing.T) {
	projectID, requestID, absent := uuid.New(), uuid.New(), uuid.New()
	logs := &fakeLogs{byRequest: func(id uuid.UUID) ([]domain.Log, error) {
		if id == absent {
			return nil, nil
		}
		return []domain.Log{appLog(requestID, time.Now())}, nil
	}}
	cs := session(t, Deps{Logs: logs, Access: fakeAccess{readable: map[uuid.UUID]bool{projectID: true}}})

	res := call(t, cs, "get_network_request", map[string]any{"project_id": projectID.String(), "request_id": requestID.String()})
	if res.IsError {
		t.Fatalf("an uncaptured call answered as an error: %s", resultText(res))
	}
	text := resultText(res)
	if !strings.Contains(text, `"phases":[]`) {
		t.Errorf("phases should be present and empty, got %s", text)
	}
	var out networkRequestResult
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.LinkedLogs) != 1 {
		t.Errorf("want the app's one log, got %+v", out.LinkedLogs)
	}
	if out.Note != domain.CallNotCaptured {
		t.Errorf("want the not-captured note, got %q", out.Note)
	}

	// Nothing at all under the id is still not found.
	gone := call(t, cs, "get_network_request", map[string]any{"project_id": projectID.String(), "request_id": absent.String()})
	if !gone.IsError || !strings.Contains(resultText(gone), "not found") {
		t.Errorf("an id with no logs at all should be not found, got %s", resultText(gone))
	}
}

// The app can put a call's request id on as many logs as it likes, one for each
// list item its model rejected. Sent whole, three hundred of those are megabytes
// in a context window, so the tool lists the earliest within the list budgets
// and counts the rest, and the call's own phases stay whole.
func TestGetNetworkRequestListsTheEarliestAppLogsAndCountsTheRest(t *testing.T) {
	projectID, requestID := uuid.New(), uuid.New()
	at := time.Now()
	body := json.RawMessage(`{"status_code":200,"body":"` + strings.Repeat("b", 4*metadataBudget) + `"}`)
	meta := json.RawMessage(`{"item":"` + strings.Repeat("x", 20*1024) + `"}`)
	logs := &fakeLogs{byRequest: func(uuid.UUID) ([]domain.Log, error) {
		response := networkLog(requestID, domain.CallPhaseResponse, at.Add(time.Millisecond))
		response.Metadata = &body
		out := []domain.Log{networkLog(requestID, domain.CallPhaseRequest, at), response}
		for i := 0; i < 300; i++ {
			l := appLog(requestID, at.Add(time.Duration(i+2)*time.Millisecond))
			l.Message = fmt.Sprintf("Could not read item %d", i)
			l.Metadata = &meta
			out = append(out, l)
		}
		return out, nil
	}}
	cs := session(t, Deps{Logs: logs, Access: fakeAccess{readable: map[uuid.UUID]bool{projectID: true}}})

	res := call(t, cs, "get_network_request", map[string]any{"project_id": projectID.String(), "request_id": requestID.String()})
	if res.IsError {
		t.Fatalf("get_network_request failed: %s", resultText(res))
	}
	text := resultText(res)
	var out struct {
		Phases []struct {
			Metadata        any  `json:"metadata"`
			MetadataOmitted bool `json:"metadata_omitted"`
		} `json:"phases"`
		LinkedLogs []struct {
			Message         string `json:"message"`
			Metadata        any    `json:"metadata"`
			MetadataOmitted bool   `json:"metadata_omitted"`
		} `json:"linked_logs"`
		LinkedLogsTotal  int  `json:"linked_logs_total"`
		LinkedLogsCapped bool `json:"linked_logs_capped"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(out.LinkedLogs) != linkedLogLimit {
		t.Fatalf("want the earliest %d of the app's logs, got %d", linkedLogLimit, len(out.LinkedLogs))
	}
	first, last := out.LinkedLogs[0].Message, out.LinkedLogs[linkedLogLimit-1].Message
	if first != "Could not read item 0" || last != fmt.Sprintf("Could not read item %d", linkedLogLimit-1) {
		t.Errorf("want the earliest, oldest first, got %q to %q", first, last)
	}
	if out.LinkedLogsTotal != 300 || !out.LinkedLogsCapped {
		t.Errorf("want all 300 counted and the list marked capped, got %d, capped %v", out.LinkedLogsTotal, out.LinkedLogsCapped)
	}
	for _, l := range out.LinkedLogs {
		if l.Metadata != nil || !l.MetadataOmitted {
			t.Fatalf("a linked log's 20 KB of metadata was sent instead of flagged: %q", l.Message)
		}
	}
	if len(out.Phases) != 2 || out.Phases[1].Metadata == nil || out.Phases[1].MetadataOmitted {
		t.Errorf("the call's response should stay whole: %+v", out.Phases)
	}
	if len(text) > 64*1024 {
		t.Errorf("one call's result is %d bytes", len(text))
	}
}

// jsonb keeps a number too large for any float64, and only a client other than
// the SDK can send one. Kept as written, it fails the decode every result goes
// through, which took a whole page of search_logs down with it. The value that
// holds it is left out, and the rest of the result is untouched.
func TestANumberNoFloat64CanHoldDoesNotFailTheResult(t *testing.T) {
	projectID, hugeID, requestID := uuid.New(), uuid.New(), uuid.New()
	// What jsonb hands back for 1e400.
	huge := json.RawMessage(`{"body":{"n":1` + strings.Repeat("0", 400) + `}}`)
	exact := json.RawMessage(exactBody)
	response := domain.CallPhaseResponse
	hugeLog := domain.Log{
		ID: hugeID, SessionID: uuid.New(), Level: "debug", Message: "Network response",
		IsNetworkCall: true, RequestID: &requestID, CallPhase: &response, Metadata: &huge,
	}
	plainLog := domain.Log{ID: uuid.New(), SessionID: uuid.New(), Level: "info", Message: "plain", Metadata: &exact}
	logs := &fakeLogs{
		listLogs: func(string, *domain.LogCursor, int) (*domain.LogListResult, error) {
			return &domain.LogListResult{Items: []domain.Log{hugeLog, plainLog}}, nil
		},
		getLog:    func(uuid.UUID) (*domain.Log, error) { return &hugeLog, nil },
		byRequest: func(uuid.UUID) ([]domain.Log, error) { return []domain.Log{hugeLog}, nil },
	}
	cs := session(t, Deps{Logs: logs, Access: fakeAccess{readable: map[uuid.UUID]bool{projectID: true}}})

	for tool, args := range map[string]map[string]any{
		"get_log":             {"project_id": projectID.String(), "log_id": hugeID.String()},
		"get_network_request": {"project_id": projectID.String(), "request_id": requestID.String()},
		"search_logs":         {"project_id": projectID.String()},
	} {
		res := call(t, cs, tool, args)
		text := resultText(res)
		if res.IsError {
			t.Errorf("%s failed over one number: %s", tool, text)
			continue
		}
		if !strings.Contains(text, hugeID.String()) {
			t.Errorf("%s left out the log itself: %s", tool, text)
		}
		if strings.Contains(text, `"n":1000`) {
			t.Errorf("%s sent a number a float64 cannot hold", tool)
		}
		if tool == "search_logs" {
			assertExactNumbers(t, tool, text)
		}
	}
}
