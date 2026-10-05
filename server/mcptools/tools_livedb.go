package mcptools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/getcodescout/code_scout/internal/domain"
	"github.com/getcodescout/code_scout/internal/live"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The database browser tools, read-only by construction. Each tool hardcodes
// its op as a string literal — "sources", "namespaces", "schema", "rows" —
// and no input struct carries an op, args, SQL or a value. The one mutating
// op in the device's vocabulary is the literal "update", which nothing here
// can produce. The device builds every statement itself from its own schema;
// the dashboard, and therefore the agent, only ever names things.
//
// liveDBRowsLimit is a server constant, never a client input: the device
// budgets a page at 512 KB and the socket kills the whole session past 1 MB,
// so a client-supplied limit would be a foot-gun aimed at the live session
// itself.
const liveDBRowsLimit = 100

// askDB sends one hardcoded read op and decodes the device's reply envelope.
func (d Deps) askDB(ctx context.Context, rawProject, rawSession, op string, args map[string]any) (*domain.LiveDBReply, error) {
	session, err := d.requireLiveSession(ctx, rawProject, rawSession)
	if err != nil {
		return nil, err
	}
	raw, err := d.Live.Ask(ctx, session.ID, live.DeviceCommand{Op: op, Args: args})
	if err != nil {
		return nil, deviceErr(ctx, err)
	}
	var reply domain.LiveDBReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, internal(ctx, err)
	}
	if !reply.OK {
		// The device's refusal is written for a person and carries nothing
		// of ours: "Unknown database", "not registered", and so on.
		return nil, errors.New(reply.Reason())
	}
	return &reply, nil
}

type liveDBSourcesIn struct {
	ProjectID     string `json:"project_id" jsonschema:"The project's UUID."`
	LiveSessionID string `json:"live_session_id" jsonschema:"A connected live session, from list_live_sessions."`
}

type liveDBSourcesOut struct {
	// Sources is what the app chose to expose via registerDatabase. Nothing
	// else on the device is reachable, and nothing at all is writable from
	// here: these tools can only read.
	Sources []domain.LiveDBSource `json:"sources"`
}

type liveDBNamespacesIn struct {
	ProjectID     string `json:"project_id" jsonschema:"The project's UUID."`
	LiveSessionID string `json:"live_session_id" jsonschema:"A connected live session."`
	DB            string `json:"db" jsonschema:"A source name from live_db_sources."`
}

type liveDBNamespacesOut struct {
	Namespaces []domain.LiveDBNamespace `json:"namespaces"`
}

type liveDBSchemaIn struct {
	ProjectID     string `json:"project_id" jsonschema:"The project's UUID."`
	LiveSessionID string `json:"live_session_id" jsonschema:"A connected live session."`
	DB            string `json:"db" jsonschema:"A source name from live_db_sources."`
	Namespace     string `json:"namespace" jsonschema:"A table, view or box from live_db_namespaces."`
}

type liveDBSchemaOut struct {
	Schema *domain.LiveDBSchema `json:"schema"`
}

type liveDBRowsIn struct {
	ProjectID     string            `json:"project_id" jsonschema:"The project's UUID."`
	LiveSessionID string            `json:"live_session_id" jsonschema:"A connected live session."`
	DB            string            `json:"db" jsonschema:"A source name from live_db_sources."`
	Namespace     string            `json:"namespace" jsonschema:"A table, view or box from live_db_namespaces."`
	Offset        int               `json:"offset,omitempty" jsonschema:"Rows to skip. Pages are 100 rows; pass 100, 200, ... while has_more is true."`
	Sort          string            `json:"sort,omitempty" jsonschema:"Column name to sort by. The device checks it against its own schema."`
	Desc          bool              `json:"desc,omitempty" jsonschema:"Sort descending. Only meaningful with sort."`
	Filters       map[string]string `json:"filters,omitempty" jsonschema:"Column name to a value the cell must contain, matched case-insensitively. There is no expression syntax."`
}

type liveDBRowsOut struct {
	Page *domain.LiveDBPage `json:"page"`
}

func (d Deps) addLiveDBTools(s *mcp.Server) {
	addTool(s, &mcp.Tool{
		Name: "live_db_sources",
		Description: "The databases a paired device's app has chosen to expose: SQLite files and " +
			"key-value stores, read straight off the phone. Nothing is stored server-side, and " +
			"these tools cannot write — editing a cell is a human dashboard action.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in liveDBSourcesIn) (*mcp.CallToolResult, liveDBSourcesOut, error) {
		var out liveDBSourcesOut
		reply, err := d.askDB(ctx, in.ProjectID, in.LiveSessionID, "sources", nil)
		if err != nil {
			return nil, out, err
		}
		out.Sources = reply.Sources
		return nil, out, nil
	})

	addTool(s, &mcp.Tool{
		Name:        "live_db_namespaces",
		Description: "One source's tables, views or boxes, as the device lists them.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in liveDBNamespacesIn) (*mcp.CallToolResult, liveDBNamespacesOut, error) {
		var out liveDBNamespacesOut
		reply, err := d.askDB(ctx, in.ProjectID, in.LiveSessionID, "namespaces", map[string]any{"db": in.DB})
		if err != nil {
			return nil, out, err
		}
		out.Namespaces = reply.Namespaces
		return nil, out, nil
	})

	addTool(s, &mcp.Tool{
		Name:        "live_db_schema",
		Description: "One namespace's columns as the device describes them, including which are redacted.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in liveDBSchemaIn) (*mcp.CallToolResult, liveDBSchemaOut, error) {
		var out liveDBSchemaOut
		reply, err := d.askDB(ctx, in.ProjectID, in.LiveSessionID, "schema",
			map[string]any{"db": in.DB, "namespace": in.Namespace})
		if err != nil {
			return nil, out, err
		}
		out.Schema = reply.Schema
		return nil, out, nil
	})

	addTool(s, &mcp.Tool{
		Name: "live_db_rows",
		Description: "A page of rows from the device, 100 at a time: the app's actual local state " +
			"at this moment. Values the app redacts arrive redacted; stopped_for_size true means " +
			"the device cut the page to protect its connection, not that the rows ran out.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in liveDBRowsIn) (*mcp.CallToolResult, liveDBRowsOut, error) {
		var out liveDBRowsOut
		args := map[string]any{
			"db":        in.DB,
			"namespace": in.Namespace,
			"limit":     liveDBRowsLimit,
		}
		if in.Offset > 0 {
			args["offset"] = in.Offset
		}
		if in.Sort != "" {
			args["sort"] = in.Sort
			args["desc"] = in.Desc
		}
		if len(in.Filters) > 0 {
			args["filters"] = in.Filters
		}
		reply, err := d.askDB(ctx, in.ProjectID, in.LiveSessionID, "rows", args)
		if err != nil {
			return nil, out, err
		}
		out.Page = reply.Page
		return nil, out, nil
	})
}
