package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/getcodescout/code_scout/app"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewServer builds the MCP server with every tool registered. Exported apart
// from the HTTP handler so tests can drive it over an in-memory transport.
func NewServer(d Deps) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "code-scout",
		Version: app.Version,
	}, nil)

	d.addProjectTools(s)
	d.addLogTools(s)
	d.addSessionTools(s)
	d.addNetworkTools(s)
	d.addLiveTools(s)
	d.addLiveDBTools(s)

	return s
}

// addTool registers a tool the way mcp.AddTool does, except that the result is
// the bytes json.Marshal wrote for the handler's output, sent as they are.
//
// mcp.AddTool checks typed output by decoding it into `any` and then sends a
// marshal of that decoded copy, so every number goes through a float64: a
// stored 12.0 reaches the agent as 12, and 9007199254740993 as
// 9007199254740992. The output schema is inferred and checked here as the SDK
// does, on a separate decode, so nothing is sent that breaks it.
func addTool[In, Out any](s *mcp.Server, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	schema, err := jsonschema.For[Out](nil)
	if err != nil {
		panic(fmt.Errorf("output schema for %q: %w", t.Name, err))
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	if err != nil {
		panic(fmt.Errorf("output schema for %q: %w", t.Name, err))
	}
	tool := *t
	tool.OutputSchema = schema

	// Out is `any` and the handler returns nil for it, which is what makes the
	// SDK leave StructuredContent and Content as they are set here.
	mcp.AddTool(s, &tool, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		res, out, err := h(ctx, req, in)
		if err != nil {
			return nil, nil, err
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return nil, nil, internal(ctx, err)
		}
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, nil, internal(ctx, err)
		}
		if err := resolved.Validate(decoded); err != nil {
			return nil, nil, internal(ctx, fmt.Errorf("validating tool output: %w", err))
		}
		if res == nil {
			res = &mcp.CallToolResult{}
		}
		res.StructuredContent = json.RawMessage(raw)
		if res.Content == nil {
			res.Content = []mcp.Content{&mcp.TextContent{Text: string(raw)}}
		}
		return res, nil, nil
	})
}

// NewHTTPHandler mounts the server for the streamable HTTP transport, mounted
// in routes.go behind RequirePersonalToken.
//
// Stateless: every POST is a complete exchange, nothing is held between
// requests, and no Mcp-Session-Id bookkeeping exists to leak. JSONResponse:
// answers are one application/json body, never a hanging SSE stream, so the
// server's 30 second WriteTimeout can never cut a response off.
func NewHTTPHandler(d Deps) http.Handler {
	server := NewServer(d)
	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			Stateless:    true,
			JSONResponse: true,
			// The SDK refuses a localhost connection carrying a non-localhost
			// Host header, a DNS-rebinding guard for auth-less local servers.
			// This endpoint is never auth-less — RequirePersonalToken sits in
			// front — and in any real deployment nginx proxies to 127.0.0.1
			// with the public Host, which is exactly the shape the guard
			// refuses. A rebinding attack cannot carry the bearer header, so
			// the guard buys nothing here and breaks every proxied instance.
			DisableLocalhostProtection: true,
		},
	)
}
