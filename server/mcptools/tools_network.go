package mcptools

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/getcodescout/code_scout/internal/domain"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listNetworkCallsIn struct {
	ProjectID string `json:"project_id" jsonschema:"The project's UUID."`
	Path      string `json:"path,omitempty" jsonschema:"Substring match on the URL path."`
	Method    string `json:"method,omitempty" jsonschema:"Exact HTTP method, e.g. GET or POST."`
	Status    string `json:"status,omitempty" jsonschema:"A class: 2xx, 3xx, 4xx, 5xx, or failed for calls that ended in a transport error."`
	SessionID string `json:"session_id,omitempty" jsonschema:"Narrow to one launch's calls."`
	Limit     int    `json:"limit,omitempty" jsonschema:"Most calls to return, newest first. Default 50, maximum 200."`
}

type networkCallItem struct {
	// RequestID correlates the call's phases; get_network_request takes it.
	RequestID  string  `json:"request_id"`
	SessionID  string  `json:"session_id"`
	Method     *string `json:"method,omitempty"`
	URL        *string `json:"url,omitempty"`
	StatusCode *int    `json:"status_code,omitempty"`
	// State is complete, error, pending (no answer yet or ever), or
	// response-only (an answer whose request phase never arrived).
	State        string    `json:"state"`
	StartedAt    time.Time `json:"started_at"`
	DurationMS   int64     `json:"duration_ms"`
	ErrorMessage *string   `json:"error_message,omitempty"`
}

type listNetworkCallsOut struct {
	Calls []networkCallItem `json:"calls"`
}

type getNetworkRequestIn struct {
	ProjectID string `json:"project_id" jsonschema:"The project's UUID."`
	RequestID string `json:"request_id" jsonschema:"The call's request id, from list_network_calls or a log row."`
}

type getNetworkRequestOut struct {
	// Phases are the call's own network logs, whole and in order: the
	// request, then its response or error. Headers and bodies live in each
	// phase's metadata, subject to whatever redaction the app configured.
	Phases []toolLog `json:"phases"`
	// LinkedLogs are logs the app wrote itself and tied to this call with its
	// request id, such as a response body its model could not decode. They
	// are not HTTP phases and say nothing about whether the call succeeded.
	// Only the earliest linkedLogLimit, with the list budgets applied.
	LinkedLogs []toolLog `json:"linked_logs"`
	// LinkedLogsTotal counts every log the app linked to the call, and
	// LinkedLogsCapped says linked_logs left some of them out.
	LinkedLogsTotal  int  `json:"linked_logs_total"`
	LinkedLogsCapped bool `json:"linked_logs_capped,omitempty"`
	// Note is set when no phase was stored and only linked logs remain.
	Note string `json:"note,omitempty"`
}

// linkedLogLimit is how many of the app's logs get_network_request lists for
// one call. The app can put a request id on as many logs as it likes, one for
// each list item its model rejected, and the result has to fit the context
// window it is read into.
const linkedLogLimit = 20

var (
	errRequestNotFound = errNotFoundFor("network request")
	errBadStatusClass  = errors.New("status must be one of 2xx, 3xx, 4xx, 5xx or failed")
)

func validStatusClass(s string) bool {
	switch s {
	case "", "2xx", "3xx", "4xx", "5xx", "failed":
		return true
	}
	return false
}

func (d Deps) addNetworkTools(s *mcp.Server) {
	addTool(s, &mcp.Tool{
		Name: "list_network_calls",
		Description: "A project's HTTP calls, one row per call with its phases already paired: " +
			"method, URL, status, duration and state. Filter by path, method, status class or session.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listNetworkCallsIn) (*mcp.CallToolResult, listNetworkCallsOut, error) {
		var out listNetworkCallsOut
		projectID, err := d.requireProject(ctx, in.ProjectID)
		if err != nil {
			return nil, out, err
		}
		// Refused rather than ignored: a typo silently matching everything
		// would read as "no failing calls", which is a lie.
		if !validStatusClass(in.Status) {
			return nil, out, errBadStatusClass
		}

		filter := domain.NetworkFilter{Path: in.Path, Method: in.Method, Status: in.Status}
		if in.SessionID != "" {
			sid, err := uuid.Parse(in.SessionID)
			if err != nil {
				return nil, out, errSessionNotFound
			}
			filter.SessionID = &sid
		}

		calls, err := d.Logs.ListNetworkCalls(ctx, projectID, filter, clampLimit(in.Limit, 50, 200))
		if err != nil {
			return nil, out, internal(ctx, err)
		}
		out.Calls = make([]networkCallItem, 0, len(calls))
		for _, c := range calls {
			item := networkCallItem{
				RequestID:    c.RequestID.String(),
				SessionID:    c.SessionID.String(),
				Method:       c.Method,
				URL:          c.URL,
				StatusCode:   c.StatusCode,
				State:        string(c.State()),
				StartedAt:    c.StartedAt,
				DurationMS:   c.Duration().Milliseconds(),
				ErrorMessage: c.ErrorMessage,
			}
			out.Calls = append(out.Calls, item)
		}
		return nil, out, nil
	})

	addTool(s, &mcp.Tool{
		Name: "get_network_request",
		Description: "One HTTP call's logs. phases are the call itself, whole: the request and its " +
			"response or error, with full headers and bodies in each phase's metadata. linked_logs " +
			"are logs the app wrote with the call's request id, for example a failure to decode " +
			"the response body; they are not phases and do not change whether the call succeeded. " +
			"linked_logs lists the earliest " + strconv.Itoa(linkedLogLimit) + ", truncated or " +
			"omitted with a flag like search_logs rows; linked_logs_total counts them all, get_log " +
			"returns one whole, and search_logs with request:UUID pages through every one. " +
			"When no phase was stored, phases is empty and note says why.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getNetworkRequestIn) (*mcp.CallToolResult, getNetworkRequestOut, error) {
		var out getNetworkRequestOut
		projectID, err := d.requireProject(ctx, in.ProjectID)
		if err != nil {
			return nil, out, err
		}
		requestID, err := uuid.Parse(in.RequestID)
		if err != nil {
			return nil, out, errRequestNotFound
		}
		logs, linkedTotal, err := d.Logs.GetNetworkRequest(ctx, projectID, requestID, linkedLogLimit)
		if err != nil {
			return nil, out, internal(ctx, err)
		}
		if len(logs) == 0 {
			return nil, out, errRequestNotFound
		}
		phases, linked := domain.SplitCallLogs(logs)
		out.Phases = make([]toolLog, 0, len(phases))
		for _, l := range phases {
			out.Phases = append(out.Phases, wholeLog(l))
		}
		out.LinkedLogs = make([]toolLog, 0, len(linked))
		for _, l := range linked {
			out.LinkedLogs = append(out.LinkedLogs, listLog(l))
		}
		out.LinkedLogsTotal = linkedTotal
		out.LinkedLogsCapped = linkedTotal > len(linked)
		if len(phases) == 0 {
			out.Note = domain.CallNotCaptured
		}
		return nil, out, nil
	})
}
