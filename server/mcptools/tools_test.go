package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/getcodescout/code_scout/internal/domain"
	"github.com/getcodescout/code_scout/pkg/search"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeLogs implements LogQuerier with pluggable behaviour per method; the
// zero value answers everything empty.
type fakeLogs struct {
	listLogs   func(query string, cursor *domain.LogCursor, limit int) (*domain.LogListResult, error)
	getLog     func(logID uuid.UUID) (*domain.Log, error)
	byRequest  func(requestID uuid.UUID) ([]domain.Log, error)
	networkErr error
}

func (f *fakeLogs) ListLogs(_ context.Context, _ uuid.UUID, query string, cursor *domain.LogCursor, limit int) (*domain.LogListResult, error) {
	if f.listLogs != nil {
		return f.listLogs(query, cursor, limit)
	}
	return &domain.LogListResult{}, nil
}

func (f *fakeLogs) GetLog(_ context.Context, _, logID uuid.UUID) (*domain.Log, error) {
	if f.getLog != nil {
		return f.getLog(logID)
	}
	return nil, domain.ErrNotFound
}

func (f *fakeLogs) GetErrorGroups(context.Context, uuid.UUID, *time.Time, int) ([]domain.ErrorGroup, error) {
	return nil, nil
}
func (f *fakeLogs) GetTagCounts(context.Context, uuid.UUID, *time.Time, int) ([]domain.TagCount, error) {
	return nil, nil
}
func (f *fakeLogs) ListSessions(context.Context, uuid.UUID, *uuid.UUID, int) ([]domain.SessionSummary, error) {
	return nil, nil
}
func (f *fakeLogs) GetSession(context.Context, uuid.UUID, uuid.UUID) (*domain.Session, error) {
	return nil, domain.ErrNotFound
}
func (f *fakeLogs) GetSessionTimeline(context.Context, uuid.UUID, uuid.UUID) ([]domain.Log, error) {
	return nil, nil
}
func (f *fakeLogs) ListDevices(context.Context, uuid.UUID, int) ([]domain.Device, error) {
	return nil, nil
}
func (f *fakeLogs) GetDevice(context.Context, uuid.UUID, uuid.UUID) (*domain.Device, error) {
	return nil, domain.ErrNotFound
}
func (f *fakeLogs) ListNetworkCalls(context.Context, uuid.UUID, domain.NetworkFilter, int) ([]domain.NetworkCall, error) {
	return nil, f.networkErr
}

// GetNetworkRequest keeps the repository's contract over whatever byRequest
// returns: every phase, the earliest linkedLimit of the app's own logs, and a
// count of all of them.
func (f *fakeLogs) GetNetworkRequest(_ context.Context, _, requestID uuid.UUID, linkedLimit int) ([]domain.Log, int, error) {
	if f.byRequest == nil {
		return nil, 0, nil
	}
	all, err := f.byRequest(requestID)
	if err != nil {
		return nil, 0, err
	}
	var logs []domain.Log
	linked := 0
	for _, l := range all {
		if !l.IsNetworkCall {
			if linked++; linked > linkedLimit {
				continue
			}
		}
		logs = append(logs, l)
	}
	return logs, linked, nil
}
func (f *fakeLogs) GetProjectOverview(_ context.Context, _ uuid.UUID, w domain.OverviewWindow) (*domain.ProjectOverview, error) {
	return &domain.ProjectOverview{Window: w}, nil
}

type fakeProjects struct{}

func (fakeProjects) ListProjects(context.Context, domain.ProjectListOpts) (*domain.ProjectListResult, int, error) {
	return &domain.ProjectListResult{}, 200, nil
}

// fakeAccess grants read on exactly the ids in readable.
type fakeAccess struct{ readable map[uuid.UUID]bool }

func (f fakeAccess) ResolveAccess(_ context.Context, _ *domain.User, projectID uuid.UUID) (domain.ProjectAccess, error) {
	if f.readable[projectID] {
		return domain.ProjectAccess{CanRead: true}, nil
	}
	return domain.ProjectAccess{}, nil
}

type fakeSettings struct{}

func (fakeSettings) Current() domain.InstanceSettings {
	return domain.InstanceSettings{RetentionDays: 30}
}

// session connects a real MCP client to the real server over an in-memory
// transport, so every call crosses genuine schema validation and the SDK's
// error packing rather than poking handlers directly.
func session(t *testing.T, d Deps) *mcp.ClientSession {
	t.Helper()
	if d.Projects == nil {
		d.Projects = fakeProjects{}
	}
	if d.Settings == nil {
		d.Settings = fakeSettings{}
	}
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := NewServer(d).Connect(context.Background(), serverT, nil); err != nil {
		t.Fatalf("connect server: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", tool, err)
	}
	return res
}

func resultText(res *mcp.CallToolResult) string {
	var out strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			out.WriteString(tc.Text)
		}
	}
	return out.String()
}

func TestAnInvisibleProjectIsIndistinguishableFromAbsent(t *testing.T) {
	hidden := uuid.New() // exists, but the caller may not read it
	cs := session(t, Deps{Logs: &fakeLogs{}, Access: fakeAccess{readable: map[uuid.UUID]bool{}}})

	invisible := call(t, cs, "search_logs", map[string]any{"project_id": hidden.String()})
	absent := call(t, cs, "search_logs", map[string]any{"project_id": uuid.NewString()})
	malformed := call(t, cs, "search_logs", map[string]any{"project_id": "not-a-uuid"})

	for name, res := range map[string]*mcp.CallToolResult{"invisible": invisible, "absent": absent, "malformed": malformed} {
		if !res.IsError {
			t.Fatalf("%s project did not error", name)
		}
	}
	if a, b := resultText(invisible), resultText(absent); a != b {
		t.Errorf("invisible and absent answer differently: %q vs %q", a, b)
	}
	if a, b := resultText(invisible), resultText(malformed); a != b {
		t.Errorf("invisible and malformed answer differently: %q vs %q", a, b)
	}
}

func TestSearchLogsSurfacesTheParsePosition(t *testing.T) {
	projectID := uuid.New()
	logs := &fakeLogs{listLogs: func(query string, _ *domain.LogCursor, _ int) (*domain.LogListResult, error) {
		// Exactly how LogQueryService wraps a parse failure.
		return nil, fmt.Errorf("invalid search query: %w", &search.ParseError{Position: 7, Message: `unknown field "lvel"`})
	}}
	cs := session(t, Deps{Logs: logs, Access: fakeAccess{readable: map[uuid.UUID]bool{projectID: true}}})

	res := call(t, cs, "search_logs", map[string]any{"project_id": projectID.String(), "query": "lvel:error"})
	if !res.IsError {
		t.Fatal("a parse error did not surface as a tool error")
	}
	text := resultText(res)
	if !strings.Contains(text, "position 7") || !strings.Contains(text, "lvel") {
		t.Errorf("the parse position did not reach the caller: %q", text)
	}
}

func TestAnUnexpectedServiceErrorIsSanitized(t *testing.T) {
	projectID := uuid.New()
	leak := "pq: password authentication failed for user code_scout"
	logs := &fakeLogs{networkErr: errors.New(leak)}
	cs := session(t, Deps{Logs: logs, Access: fakeAccess{readable: map[uuid.UUID]bool{projectID: true}}})

	res := call(t, cs, "list_network_calls", map[string]any{"project_id": projectID.String()})
	if !res.IsError {
		t.Fatal("the failure did not surface at all")
	}
	text := resultText(res)
	if strings.Contains(text, "pq:") || strings.Contains(text, "password") {
		t.Errorf("internals crossed the wire: %q", text)
	}
	if !strings.Contains(text, "internal error") {
		t.Errorf("expected a bare internal error, got %q", text)
	}
}

func TestGetLogReturnsWhatTheListOmitted(t *testing.T) {
	projectID, logID := uuid.New(), uuid.New()
	big := json.RawMessage(`{"body":"` + strings.Repeat("x", metadataBudget*2) + `"}`)
	stored := domain.Log{ID: logID, SessionID: uuid.New(), Level: "info", Message: "m", Metadata: &big}

	logs := &fakeLogs{
		listLogs: func(string, *domain.LogCursor, int) (*domain.LogListResult, error) {
			return &domain.LogListResult{Items: []domain.Log{stored}}, nil
		},
		getLog: func(id uuid.UUID) (*domain.Log, error) {
			if id != logID {
				return nil, domain.ErrNotFound
			}
			return &stored, nil
		},
	}
	cs := session(t, Deps{Logs: logs, Access: fakeAccess{readable: map[uuid.UUID]bool{projectID: true}}})

	listed := call(t, cs, "search_logs", map[string]any{"project_id": projectID.String()})
	if !strings.Contains(resultText(listed), `"metadata_omitted":true`) {
		t.Fatalf("the list did not flag the omission: %s", resultText(listed))
	}
	if strings.Contains(resultText(listed), `xxxxxxxxxx`) {
		t.Fatal("the list carried the oversize body anyway")
	}

	whole := call(t, cs, "get_log", map[string]any{"project_id": projectID.String(), "log_id": logID.String()})
	if whole.IsError {
		t.Fatalf("get_log failed: %s", resultText(whole))
	}
	if !strings.Contains(resultText(whole), strings.Repeat("x", metadataBudget*2)) {
		t.Error("get_log did not return the full metadata")
	}
}

func TestABadStatusClassIsRefusedNotIgnored(t *testing.T) {
	projectID := uuid.New()
	cs := session(t, Deps{Logs: &fakeLogs{}, Access: fakeAccess{readable: map[uuid.UUID]bool{projectID: true}}})

	res := call(t, cs, "list_network_calls", map[string]any{"project_id": projectID.String(), "status": "400"})
	if !res.IsError {
		t.Fatal("a bad status class was silently accepted, which would read as \"no failing calls\"")
	}
	if !strings.Contains(resultText(res), "2xx") {
		t.Errorf("the refusal should name the valid classes: %q", resultText(res))
	}
}

func TestAMangledCursorIsAToolError(t *testing.T) {
	projectID := uuid.New()
	cs := session(t, Deps{Logs: &fakeLogs{}, Access: fakeAccess{readable: map[uuid.UUID]bool{projectID: true}}})

	res := call(t, cs, "search_logs", map[string]any{"project_id": projectID.String(), "cursor": "garbage"})
	if !res.IsError || !strings.Contains(resultText(res), "next_cursor") {
		t.Errorf("a mangled cursor should fail naming the fix: %q", resultText(res))
	}
}

// The read-only guarantee, pinned structurally: no registered tool may be
// write-shaped by name, and no input schema may carry a field a write or a
// statement could travel through. Slice D's live database tools rely on this
// walk staying green.
func TestNoToolCanExpressAWrite(t *testing.T) {
	cs := session(t, Deps{Logs: &fakeLogs{}, Access: fakeAccess{}})

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("no tools registered, so this test proves nothing")
	}

	writeShaped := []string{"update", "write", "set_", "delete", "insert", "create", "exec"}
	forbiddenFields := map[string]bool{"op": true, "args": true, "sql": true, "statement": true, "query_sql": true, "value": true}

	for _, tool := range tools.Tools {
		for _, verb := range writeShaped {
			if strings.Contains(tool.Name, verb) {
				t.Errorf("tool %q is write-shaped", tool.Name)
			}
		}

		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal schema of %s: %v", tool.Name, err)
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("unmarshal schema of %s: %v", tool.Name, err)
		}
		if len(schema.Properties) == 0 && tool.Name != "list_projects" {
			t.Errorf("tool %q has no input schema to inspect", tool.Name)
		}
		for field := range schema.Properties {
			if forbiddenFields[field] {
				t.Errorf("tool %q input carries forbidden field %q", tool.Name, field)
			}
		}
	}
}
