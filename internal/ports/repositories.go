package ports

import (
	"context"
	"time"

	"github.com/getcodescout/code_scout/internal/domain"
	"github.com/google/uuid"
)

type ProjectRepository interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Project, error)
	GetByName(ctx context.Context, name string) (*domain.Project, error)
	Create(ctx context.Context, project *domain.Project) error
	Update(ctx context.Context, project *domain.Project) error
	Delete(ctx context.Context, project *domain.Project) error
	// LockProject takes a row-level write lock. Only meaningful in a transaction.
	LockProject(ctx context.Context, id uuid.UUID) (*domain.Project, error)
	GetSecret(ctx context.Context, projectID uuid.UUID) (*domain.ProjectSecret, error)
	CreateSecret(ctx context.Context, secret *domain.ProjectSecret) error
	DeleteSecret(ctx context.Context, secret *domain.ProjectSecret) error
	ReplaceSecret(ctx context.Context, projectID uuid.UUID, secretKey string) (*domain.ProjectSecret, error)
	DeleteSecretsByProject(ctx context.Context, projectID uuid.UUID) (int64, error)
	DeleteFavoritesByProject(ctx context.Context, projectID uuid.UUID) (int64, error)
	List(ctx context.Context, opts domain.ProjectListOpts) (*domain.ProjectListResult, error)
	SetFavorite(ctx context.Context, userID, projectID uuid.UUID, favorite bool) error
	IsFavorite(ctx context.Context, userID, projectID uuid.UUID) (bool, error)
}

// MemberRepository owns project membership, which is what decides visibility
// for everyone except a super admin.
type MemberRepository interface {
	GetMembership(ctx context.Context, userID, projectID uuid.UUID) (*domain.ProjectMember, error)
	SetMembership(ctx context.Context, userID, projectID uuid.UUID, level domain.ProjectLevel) error
	RemoveMembership(ctx context.Context, userID, projectID uuid.UUID) error
	ListProjectMembers(ctx context.Context, projectID uuid.UUID) ([]domain.ProjectMember, error)
	ListUserProjectIDs(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error)
	RemoveAllForUser(ctx context.Context, userID uuid.UUID) error
	RemoveAllForProject(ctx context.Context, projectID uuid.UUID) error
}

type LogRepository interface {
	// CreateBatch returns the number of rows actually inserted, which is not
	// len(logs) when a retried batch is deduplicated away.
	CreateBatch(ctx context.Context, logs []domain.Log) (int64, error)
	List(ctx context.Context, opts domain.LogListOpts) (*domain.LogListResult, error)
	// GetByID is one log, project-scoped in the query so a foreign id reads
	// as domain.ErrNotFound rather than as another project's row.
	GetByID(ctx context.Context, projectID, logID uuid.UUID) (*domain.Log, error)
	GetBySessionID(ctx context.Context, projectID, sessionID uuid.UUID, limit int) ([]domain.Log, error)
	// GetByRequestID is every phase of one call and the earliest linkedLimit
	// of the logs the app linked to it, oldest first. linkedTotal counts all of
	// the app's, listed or not.
	GetByRequestID(ctx context.Context, projectID, requestID uuid.UUID, linkedLimit int) (logs []domain.Log, linkedTotal int, err error)
	GetStats(ctx context.Context, opts domain.LogStatsOpts) (*domain.LogStatsResult, error)
	// GetOverview counts the tiles and chart on the project overview.
	GetOverview(ctx context.Context, projectID uuid.UUID, window domain.OverviewWindow) (*domain.ProjectOverview, error)
	// GetTagCounts lists the tags in use, for the log viewer's tag picker.
	GetTagCounts(ctx context.Context, projectID uuid.UUID, since *time.Time, limit int) ([]domain.TagCount, error)
	// ListNetworkCalls collapses the three phase logs into one row per call.
	ListNetworkCalls(ctx context.Context, projectID uuid.UUID, f domain.NetworkFilter, limit int) ([]domain.NetworkCall, error)
	// GetErrorGroups collapses errors into distinct problems for the Errors screen.
	GetErrorGroups(ctx context.Context, projectID uuid.UUID, since *time.Time, limit int) ([]domain.ErrorGroup, error)
	SoftDeleteBefore(ctx context.Context, before time.Time) (int64, error)
	PurgeSoftDeleted(ctx context.Context, olderThan time.Time) (int64, error)
	// PurgeOrphanedLogs removes up to limit rows belonging to deleted projects.
	PurgeOrphanedLogs(ctx context.Context, limit int) (int64, error)
}

type UserRepository interface {
	Count(ctx context.Context) (int64, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error)
	GetByEmail(ctx context.Context, email string) (*domain.User, error)
	List(ctx context.Context) ([]domain.User, error)
	UpdatePasswordHash(ctx context.Context, userID uuid.UUID, hash string) error
	SetMustChangePassword(ctx context.Context, userID uuid.UUID, must bool) error
	UpdateRole(ctx context.Context, userID uuid.UUID, role string) error
	Delete(ctx context.Context, userID uuid.UUID) error
	// CountSuperAdminsForUpdate locks EVERY super admin row it counts. Only
	// meaningful inside a transaction; locking the whole set (not just some
	// subset) is what gives two concurrent removals a shared resource to
	// serialise on.
	CountSuperAdminsForUpdate(ctx context.Context) (int64, error)
	DeleteSessionsByUserID(ctx context.Context, userID uuid.UUID) error
	Create(ctx context.Context, user *domain.User) error
	CreateSession(ctx context.Context, session *domain.UserSession) error
	GetSessionByToken(ctx context.Context, token string) (*domain.UserSession, error)
	DeleteSession(ctx context.Context, token string) error
}

// TokenRepository owns personal access tokens, the MCP endpoint's credential.
// Only hashes are stored; nothing here ever sees a plaintext token.
type TokenRepository interface {
	Create(ctx context.Context, token *domain.PersonalAccessToken) error
	GetByHash(ctx context.Context, hash string) (*domain.PersonalAccessToken, error)
	ListByUser(ctx context.Context, userID uuid.UUID) ([]domain.PersonalAccessToken, error)
	// Count is live tokens only, for the per-user cap.
	Count(ctx context.Context, userID uuid.UUID) (int64, error)
	// Revoke is owner-scoped: a token id alone cannot revoke another user's.
	Revoke(ctx context.Context, userID, tokenID uuid.UUID) error
	// TouchLastUsed writes only when the stored stamp is older than since, so
	// authenticating is not one UPDATE per request.
	TouchLastUsed(ctx context.Context, tokenID uuid.UUID, at time.Time, since time.Time) error
	DeleteByUser(ctx context.Context, userID uuid.UUID) error
}

// InstanceSettingsRepository stores the single row of runtime configuration.
type InstanceSettingsRepository interface {
	// Get returns the stored settings, or the defaults when none are saved yet.
	Get(ctx context.Context) (*domain.InstanceSettings, error)
	Save(ctx context.Context, settings *domain.InstanceSettings) error
}

// UsageRepository counts what a project has stored, per UTC day.
type UsageRepository interface {
	CountForDay(ctx context.Context, projectID uuid.UUID, day time.Time) (int64, error)
	// Add is called inside the same transaction as the insert it counts.
	Add(ctx context.Context, projectID uuid.UUID, day time.Time, n int64) error
	PurgeUsageBefore(ctx context.Context, day time.Time) (int64, error)
}

// SessionRepository stores one row per app launch.
type SessionRepository interface {
	// Upsert records a session, or revises the one already stored. The SDK
	// re-sends it with every batch, so a repeat is the normal case.
	Upsert(ctx context.Context, session *domain.Session) error
	GetByID(ctx context.Context, projectID, sessionID uuid.UUID) (*domain.Session, error)
	// List is newest first. A non-nil installationID narrows it to one device.
	List(ctx context.Context, projectID uuid.UUID, installationID *uuid.UUID, limit int) ([]domain.SessionSummary, error)
	// Counts is how many launches and how many distinct people, across
	// everything rather than across the capped list.
	Counts(ctx context.Context, projectID uuid.UUID) (sessions int64, users int64, err error)
	ListDevices(ctx context.Context, projectID uuid.UUID, limit int) ([]domain.Device, error)
	GetDevice(ctx context.Context, projectID, installationID uuid.UUID) (*domain.Device, error)

	// PurgeSessionsBefore drops sessions last seen before a cutoff, returning
	// how many went. One row per app launch per device and nothing else ever
	// deletes from the table, so without this it is the one part of the
	// database that grows forever no matter what retention is set to.
	PurgeSessionsBefore(ctx context.Context, before time.Time) (int64, error)

	// PurgeOrphanedSessions drops sessions whose project has been deleted, in
	// batches, the same way logs are reaped. Call again while it returns limit.
	PurgeOrphanedSessions(ctx context.Context, limit int) (int64, error)
}
