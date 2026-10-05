package mcptools

import (
	"context"
	"time"

	"github.com/getcodescout/code_scout/internal/domain"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// searchSyntax is the whole operator reference, embedded in the tool
// description so an agent needs no second lookup to write a correct query.
// It is the same syntax as the dashboard's search box, verbatim.
const searchSyntax = "Query syntax (all clauses AND together): " +
	"level:error (repeatable, ORed; one of info debug warning error fatal verbose system) | " +
	"tag:checkout (repeatable, ANDed) and -tag:noise to exclude | " +
	"is:network for network phases only | " +
	"last:1h, last:24h, last:7d or last:30d | " +
	"session:UUID for one launch | " +
	"request:UUID for one network call's phases plus any log the app linked to it, such as a failure to decode its body | " +
	"fingerprint:\"...\" for one error group | " +
	"user:alice, installation:UUID, app_version:3.2.1, sdk_version:1.5.0 match the log's " +
	"whole session exactly; device:pixel and os:android match as substrings | " +
	"bare words or \"a quoted phrase\" search the message text."

type searchLogsIn struct {
	ProjectID string `json:"project_id" jsonschema:"The project's UUID, from list_projects."`
	Query     string `json:"query,omitempty" jsonschema:"Search query. Empty returns everything newest first."`
	Cursor    string `json:"cursor,omitempty" jsonschema:"The next_cursor from the previous page. Omit for the first page."`
	Limit     int    `json:"limit,omitempty" jsonschema:"Logs per page. Default 50, maximum 200."`
}

type searchLogsOut struct {
	Logs []toolLog `json:"logs"`
	// NextCursor pages forward; absent on the last page. Long messages,
	// metadata and stack traces are truncated or omitted here, each with a
	// flag saying so; get_log returns one log whole.
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}

type getLogIn struct {
	ProjectID string `json:"project_id" jsonschema:"The project's UUID."`
	LogID     string `json:"log_id" jsonschema:"The log's id, from search_logs or an error group's latest_log_id."`
}

type getLogOut struct {
	Log toolLog `json:"log"`
}

type errorGroupsIn struct {
	ProjectID  string `json:"project_id" jsonschema:"The project's UUID."`
	SinceHours int    `json:"since_hours,omitempty" jsonschema:"Only groups seen in the last N hours. Omit for all time."`
	Limit      int    `json:"limit,omitempty" jsonschema:"Most groups to return, most frequent first. Default 20, maximum 50."`
}

type errorGroup struct {
	// Fingerprint is the grouping key. SearchQuery is it already quoted for
	// search_logs, which lists every occurrence.
	Fingerprint   string    `json:"fingerprint"`
	SearchQuery   string    `json:"search_query"`
	SampleMessage string    `json:"sample_message"`
	Level         string    `json:"level"`
	Count         int64     `json:"count"`
	Sessions      int64     `json:"sessions"`
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`
	// LatestLogID fetches the newest occurrence whole via get_log;
	// LatestSessionID hands its launch to get_session_timeline.
	LatestLogID       string `json:"latest_log_id"`
	LatestSessionID   string `json:"latest_session_id"`
	StackTrace        any    `json:"stack_trace,omitempty"`
	StackTraceOmitted bool   `json:"stack_trace_omitted,omitempty"`
	Tags              any    `json:"tags,omitempty"`
}

type errorGroupsOut struct {
	Groups []errorGroup `json:"groups"`
}

func (d Deps) addLogTools(s *mcp.Server) {
	addTool(s, &mcp.Tool{
		Name:        "search_logs",
		Description: "Search one project's logs, newest first, with keyset pagination. " + searchSyntax,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchLogsIn) (*mcp.CallToolResult, searchLogsOut, error) {
		var out searchLogsOut
		projectID, err := d.requireProject(ctx, in.ProjectID)
		if err != nil {
			return nil, out, err
		}
		cursor, err := decodeCursor(in.Cursor)
		if err != nil {
			return nil, out, err
		}

		res, err := d.Logs.ListLogs(ctx, projectID, in.Query, cursor, clampLimit(in.Limit, 50, 200))
		if err != nil {
			return nil, out, parseSearchErr(ctx, err)
		}

		out.HasMore = res.HasMore
		out.NextCursor = encodeCursor(res.NextCursor)
		out.Logs = make([]toolLog, 0, len(res.Items))
		for _, l := range res.Items {
			out.Logs = append(out.Logs, listLog(l))
		}
		return nil, out, nil
	})

	addTool(s, &mcp.Tool{
		Name: "get_log",
		Description: "One log, whole: the full message, metadata and stack trace that " +
			"search_logs may have truncated or omitted.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getLogIn) (*mcp.CallToolResult, getLogOut, error) {
		var out getLogOut
		projectID, err := d.requireProject(ctx, in.ProjectID)
		if err != nil {
			return nil, out, err
		}
		logID, err := uuid.Parse(in.LogID)
		if err != nil {
			return nil, out, errLogNotFound
		}
		l, err := d.Logs.GetLog(ctx, projectID, logID)
		if err != nil {
			if err == domain.ErrNotFound {
				return nil, out, errLogNotFound
			}
			return nil, out, internal(ctx, err)
		}
		out.Log = wholeLog(*l)
		return nil, out, nil
	})

	addTool(s, &mcp.Tool{
		Name: "get_error_groups",
		Description: "A project's errors and fatals grouped into distinct problems, most frequent " +
			"first, each with its latest stack trace. The place to start on \"what is broken?\".",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in errorGroupsIn) (*mcp.CallToolResult, errorGroupsOut, error) {
		var out errorGroupsOut
		projectID, err := d.requireProject(ctx, in.ProjectID)
		if err != nil {
			return nil, out, err
		}
		groups, err := d.Logs.GetErrorGroups(ctx, projectID, sinceHours(in.SinceHours), clampLimit(in.Limit, 20, 50))
		if err != nil {
			return nil, out, internal(ctx, err)
		}

		out.Groups = make([]errorGroup, 0, len(groups))
		for _, g := range groups {
			fp := g.Fingerprint
			item := errorGroup{
				Fingerprint:     g.Fingerprint,
				SearchQuery:     domain.SearchFilter{Fingerprint: &fp}.Query(),
				SampleMessage:   g.SampleMessage,
				Level:           g.Level,
				Count:           g.Count,
				Sessions:        g.Sessions,
				FirstSeen:       g.FirstSeen,
				LastSeen:        g.LastSeen,
				LatestLogID:     g.LatestLogID.String(),
				LatestSessionID: g.LatestSessionID.String(),
			}
			item.Tags = rawJSON(g.Tags)
			if g.StackTrace != nil {
				if len(*g.StackTrace) > stackBudget {
					item.StackTraceOmitted = true
				} else {
					item.StackTrace = rawJSON(g.StackTrace)
				}
			}
			out.Groups = append(out.Groups, item)
		}
		return nil, out, nil
	})
}

var errLogNotFound = errNotFoundFor("log")
