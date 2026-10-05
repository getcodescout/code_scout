package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getcodescout/code_scout/internal/domain"
	"github.com/getcodescout/code_scout/server/middleware"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This file pins the SDK behaviours the endpoint's design leans on, over real
// HTTP. If an SDK upgrade changes any of them, the design has to be
// revisited, and these say so before production does.

type emptyIn struct{}

func shapeServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "shape-test"}, nil)
	// who reports the user the HTTP middleware put on the request context.
	mcp.AddTool(s, &mcp.Tool{Name: "who", Description: "context probe"},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, any, error) {
			name := "nobody"
			if u := middleware.UserFrom(ctx); u != nil {
				name = u.Name
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: name}}}, nil, nil
		})
	// boom returns a raw Go error, the way a careless handler would.
	mcp.AddTool(s, &mcp.Tool{Name: "boom", Description: "error probe"},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ emptyIn) (*mcp.CallToolResult, any, error) {
			return nil, nil, errors.New("pq: password authentication failed")
		})
	// numbers returns two numbers as a stored body wrote them, the way
	// rawJSON hands them to a tool.
	mcp.AddTool(s, &mcp.Tool{Name: "numbers", Description: "number probe"},
		func(context.Context, *mcp.CallToolRequest, emptyIn) (*mcp.CallToolResult, numbersOut, error) {
			return nil, numbersOut{Price: json.Number("12.0"), ID: json.Number("9007199254740993")}, nil
		})
	return s
}

type numbersOut struct {
	Price any `json:"price"`
	ID    any `json:"id"`
}

func shapeHTTP(t *testing.T) *httptest.Server {
	t.Helper()
	inner := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return shapeServer() },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	// A stand-in for RequirePersonalToken: put a user on the request context
	// the same way it does.
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := middleware.WithUser(r.Context(), &domain.User{Name: "Ada"})
		inner.ServeHTTP(w, r.WithContext(ctx))
	})
	ts := httptest.NewServer(wrapped)
	t.Cleanup(ts.Close)
	return ts
}

func shapeSession(t *testing.T, ts *httptest.Server) *mcp.ClientSession {
	t.Helper()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "shape-client"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
	if err != nil {
		t.Fatalf("connect over HTTP: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// Pin 1: context values set by our HTTP middleware are visible inside tool
// handlers. Everything about auth rides on this.
func TestHTTPMiddlewareContextReachesToolHandlers(t *testing.T) {
	cs := shapeSession(t, shapeHTTP(t))
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "who", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if got := resultText(res); got != "Ada" {
		t.Errorf("the tool saw %q; the middleware's user did not reach it", got)
	}
}

// Pin 2: a Go error returned from a typed handler is packed INTO the result,
// text and all, as an IsError tool result. This is why internal() sanitises
// at the source: whatever a handler returns is what the client reads.
func TestAReturnedGoErrorReachesTheClientVerbatim(t *testing.T) {
	cs := shapeSession(t, shapeHTTP(t))
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "boom", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError {
		t.Fatal("a returned error did not become an IsError result")
	}
	if !strings.Contains(resultText(res), "pq: password authentication failed") {
		t.Errorf("expected the raw text to cross the wire (the behaviour we sanitise against), got %q", resultText(res))
	}
}

// Pin 4: a request that arrives on a loopback socket carrying a public Host
// header — every reverse-proxied deployment, dev.codescout.in included — is
// served, not refused. The SDK's DNS-rebinding guard rejects exactly that
// shape unless DisableLocalhostProtection is set, and the first deploy behind
// nginx died on it with `Forbidden: invalid Host header`.
func TestAProxiedHostHeaderIsNotRefused(t *testing.T) {
	// The REAL production handler, not the shape-test one: the guard is
	// disabled in NewHTTPHandler's options, and that is what this pins.
	ts := httptest.NewServer(NewHTTPHandler(Deps{Logs: &fakeLogs{}, Access: fakeAccess{}}))
	t.Cleanup(ts.Close) // listens on 127.0.0.1, like the app behind nginx

	req, _ := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	req.Host = "dev.codescout.in" // what nginx forwards
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusForbidden {
		t.Fatalf("the proxied Host header was refused: %s", body)
	}
	if !strings.Contains(string(body), `"tools"`) {
		t.Errorf("expected a tools/list result, got %d: %.200s", resp.StatusCode, body)
	}
}

// Pin 3: in stateless JSON mode a POST is answered with one application/json
// body, never a hanging text/event-stream — which is what makes the server's
// 30 second WriteTimeout safe to share.
func TestStatelessJSONAnswersOneBody(t *testing.T) {
	ts := shapeHTTP(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"raw","version":"0"}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if !strings.Contains(string(body), `"jsonrpc"`) {
		t.Errorf("expected one JSON-RPC body, got: %s", body)
	}
}

// Pin 5: mcp.AddTool checks typed output by decoding it into `any`, then sends
// a marshal of that decoded copy, so every number crosses a float64 on its way
// out. A body's 12.0 arrives as 12 and an id past 2^53 arrives rounded, which
// is why every tool here registers through addTool instead. If this starts
// failing, the SDK sends numbers as written and addTool can go.
func TestTheSDKSendsEveryNumberThroughAFloat64(t *testing.T) {
	cs := shapeSession(t, shapeHTTP(t))
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "numbers", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if got := resultText(res); got != `{"id":9007199254740992,"price":12}` {
		t.Errorf("the SDK no longer rewrites numbers, got %s", got)
	}
}
