package mcptools

import (
	"context"
	"time"

	"github.com/getcodescout/code_scout/internal/domain"
	"github.com/getcodescout/code_scout/server/middleware"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listProjectsIn struct {
	Search string `json:"search,omitempty" jsonschema:"Optional name filter, matched as a substring."`
	Limit  int    `json:"limit,omitempty" jsonschema:"Most projects to return. Default 50, maximum 200."`
}

type projectItem struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type listProjectsOut struct {
	Projects []projectItem `json:"projects"`
	// Total is how many exist, which can exceed len(projects).
	Total int64 `json:"total"`
}

type overviewIn struct {
	ProjectID string `json:"project_id" jsonschema:"The project's UUID, from list_projects."`
	Window    string `json:"window,omitempty" jsonschema:"How far back: 24h, 7d or 30d. Default 24h. Windows past this instance's retention are clamped."`
}

type overviewBucket struct {
	Start   time.Time `json:"start"`
	Total   int64     `json:"total"`
	Errors  int64     `json:"errors"`
	Network int64     `json:"network"`
	Failed  int64     `json:"failed"`
}

type overviewOut struct {
	Window     string `json:"window"`
	Logs       int64  `json:"logs"`
	Errors     int64  `json:"errors"`
	Network    int64  `json:"network_calls"`
	Failed     int64  `json:"failed_calls"`
	Sessions   int64  `json:"sessions"`
	PrevLogs   int64  `json:"previous_period_logs"`
	PrevErrors int64  `json:"previous_period_errors"`
	// EverLogged false means the project has never reported at all, which
	// reads very differently from a quiet window.
	EverLogged     bool             `json:"ever_logged"`
	PeakErrorHour  *time.Time       `json:"peak_error_bucket,omitempty"`
	PeakErrorCount int64            `json:"peak_error_count,omitempty"`
	Buckets        []overviewBucket `json:"buckets"`
}

type tagCountsIn struct {
	ProjectID  string `json:"project_id" jsonschema:"The project's UUID."`
	SinceHours int    `json:"since_hours,omitempty" jsonschema:"Only count logs newer than this many hours. Omit for all time."`
	Limit      int    `json:"limit,omitempty" jsonschema:"Most tags to return. Default 50, maximum 200."`
}

type tagCount struct {
	Tag   string `json:"tag"`
	Count int64  `json:"count"`
}

type tagCountsOut struct {
	Tags []tagCount `json:"tags"`
}

func (d Deps) addProjectTools(s *mcp.Server) {
	addTool(s, &mcp.Tool{
		Name: "list_projects",
		Description: "List the projects this token's user can see, newest first. " +
			"Every other tool takes one of these ids as project_id.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listProjectsIn) (*mcp.CallToolResult, listProjectsOut, error) {
		var out listProjectsOut
		opts := domain.ProjectListOpts{
			Search:   in.Search,
			Page:     1,
			PageSize: clampLimit(in.Limit, 50, 200),
		}
		// The same visibility rule as the dashboard's project grid: everyone
		// sees their memberships, a super admin sees everything.
		opts.ScopeToUser(middleware.UserFrom(ctx))

		res, _, err := d.Projects.ListProjects(ctx, opts)
		if err != nil {
			return nil, out, internal(ctx, err)
		}
		out.Total = res.TotalCount
		out.Projects = make([]projectItem, 0, len(res.Items))
		for _, p := range res.Items {
			out.Projects = append(out.Projects, projectItem{
				ID: p.ID.String(), Name: p.Name, Description: p.Description, CreatedAt: p.CreatedAt,
			})
		}
		return nil, out, nil
	})

	addTool(s, &mcp.Tool{
		Name: "get_project_overview",
		Description: "Counts and an activity chart for one project over a window: logs, errors, " +
			"network calls, sessions, the previous period for comparison, and the bucket where " +
			"errors peaked. Start here to judge how a project is doing.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in overviewIn) (*mcp.CallToolResult, overviewOut, error) {
		var out overviewOut
		projectID, err := d.requireProject(ctx, in.ProjectID)
		if err != nil {
			return nil, out, err
		}

		window := domain.OverviewWindowFor(in.Window, d.Settings.Current().RetentionDays)
		ov, err := d.Logs.GetProjectOverview(ctx, projectID, window)
		if err != nil {
			return nil, out, internal(ctx, err)
		}

		out = overviewOut{
			Window:     ov.Window.Key,
			Logs:       ov.Logs,
			Errors:     ov.Errors,
			Network:    ov.Network,
			Failed:     ov.Failed,
			Sessions:   ov.Sessions,
			PrevLogs:   ov.PrevLogs,
			PrevErrors: ov.PrevErrors,
			EverLogged: ov.EverLogged,
		}
		if ov.PeakErrorCount > 0 {
			peak := ov.PeakErrorHour
			out.PeakErrorHour = &peak
			out.PeakErrorCount = ov.PeakErrorCount
		}
		out.Buckets = make([]overviewBucket, 0, len(ov.Buckets))
		for _, b := range ov.Buckets {
			out.Buckets = append(out.Buckets, overviewBucket{
				Start: b.Hour, Total: b.TotalCount, Errors: b.ErrorCount,
				Network: b.NetworkCount, Failed: b.FailedCount,
			})
		}
		return nil, out, nil
	})

	addTool(s, &mcp.Tool{
		Name: "get_tag_counts",
		Description: "The tags in use on a project's logs and how often each occurs. " +
			"Useful before search_logs, whose tag: operator takes these values.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in tagCountsIn) (*mcp.CallToolResult, tagCountsOut, error) {
		var out tagCountsOut
		projectID, err := d.requireProject(ctx, in.ProjectID)
		if err != nil {
			return nil, out, err
		}
		counts, err := d.Logs.GetTagCounts(ctx, projectID, sinceHours(in.SinceHours), clampLimit(in.Limit, 50, 200))
		if err != nil {
			return nil, out, internal(ctx, err)
		}
		out.Tags = make([]tagCount, 0, len(counts))
		for _, c := range counts {
			out.Tags = append(out.Tags, tagCount{Tag: c.Tag, Count: c.Count})
		}
		return nil, out, nil
	})
}
