// Package mcptools is the MCP endpoint: read-only tools over the same
// services the dashboard's screens use, authenticated by a personal access
// token and scoped by the same access rules as every page.
//
// It is a driving adapter, the same layer as server/handlers. Read-only is
// structural, exactly as it is for the live database browser: no tool takes
// an op, a statement, or anything a write could be smuggled through, so the
// property cannot regress without adding a tool, and a test walks the
// registered tools asserting none is write-shaped.
package mcptools

import (
	"context"
	"encoding/json"
	"time"

	"github.com/getcodescout/code_scout/internal/domain"
	"github.com/getcodescout/code_scout/internal/live"
	"github.com/google/uuid"
)

// LogQuerier is the slice of *services.LogQueryService the tools call.
// An interface so tool handlers are testable without Postgres.
type LogQuerier interface {
	ListLogs(ctx context.Context, projectID uuid.UUID, query string, cursor *domain.LogCursor, limit int) (*domain.LogListResult, error)
	GetLog(ctx context.Context, projectID, logID uuid.UUID) (*domain.Log, error)
	GetErrorGroups(ctx context.Context, projectID uuid.UUID, since *time.Time, limit int) ([]domain.ErrorGroup, error)
	GetTagCounts(ctx context.Context, projectID uuid.UUID, since *time.Time, limit int) ([]domain.TagCount, error)
	ListSessions(ctx context.Context, projectID uuid.UUID, installationID *uuid.UUID, limit int) ([]domain.SessionSummary, error)
	GetSession(ctx context.Context, projectID, sessionID uuid.UUID) (*domain.Session, error)
	GetSessionTimeline(ctx context.Context, projectID, sessionID uuid.UUID) ([]domain.Log, error)
	ListDevices(ctx context.Context, projectID uuid.UUID, limit int) ([]domain.Device, error)
	GetDevice(ctx context.Context, projectID, installationID uuid.UUID) (*domain.Device, error)
	ListNetworkCalls(ctx context.Context, projectID uuid.UUID, f domain.NetworkFilter, limit int) ([]domain.NetworkCall, error)
	GetNetworkRequest(ctx context.Context, projectID, requestID uuid.UUID, linkedLimit int) ([]domain.Log, int, error)
	GetProjectOverview(ctx context.Context, projectID uuid.UUID, window domain.OverviewWindow) (*domain.ProjectOverview, error)
}

// ProjectLister is the slice of ports.ProjectManager list_projects needs.
type ProjectLister interface {
	ListProjects(ctx context.Context, opts domain.ProjectListOpts) (*domain.ProjectListResult, int, error)
}

// AccessResolver decides visibility, implemented by *services.MemberService —
// the same resolver the web router's middleware uses, so the two surfaces
// cannot drift.
type AccessResolver interface {
	ResolveAccess(ctx context.Context, actor *domain.User, projectID uuid.UUID) (domain.ProjectAccess, error)
}

// SettingsReader supplies retention, which bounds the overview windows.
type SettingsReader interface {
	Current() domain.InstanceSettings
}

// LiveHub is the slice of *live.Hub the live tools consume. In-memory and
// in-process only — which is the reason the MCP endpoint lives inside the
// dashboard server at all.
type LiveHub interface {
	List(projectID uuid.UUID) []domain.LiveSession
	Get(sessionID uuid.UUID) (domain.LiveSession, bool)
	// Watch's subscription MUST be cancelled immediately after its backlog is
	// read: a tool is a one-shot reader, never a streaming watcher.
	Watch(sessionID uuid.UUID, afterSeq int64) (*live.Subscription, error)
	Ask(ctx context.Context, sessionID uuid.UUID, cmd live.DeviceCommand) (json.RawMessage, error)
}

// Deps is everything the tools call, wired once in main.go from the same
// instances the dashboard handlers use.
type Deps struct {
	Logs     LogQuerier
	Projects ProjectLister
	Access   AccessResolver
	Settings SettingsReader
	Live     LiveHub
}
