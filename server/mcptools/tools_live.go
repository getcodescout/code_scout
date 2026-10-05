package mcptools

import (
	"context"
	"errors"
	"time"

	"github.com/getcodescout/code_scout/internal/domain"
	"github.com/getcodescout/code_scout/internal/live"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var errLiveNotFound = errNotFoundFor("live session")

// requireLiveSession applies the project gate and then the session-belongs-
// to-project check, the same two steps as the dashboard's live handlers. A
// live session from another project answers exactly like one that never
// existed.
func (d Deps) requireLiveSession(ctx context.Context, rawProject, rawSession string) (domain.LiveSession, error) {
	projectID, err := d.requireProject(ctx, rawProject)
	if err != nil {
		return domain.LiveSession{}, err
	}
	sid, err := uuid.Parse(rawSession)
	if err != nil {
		return domain.LiveSession{}, errLiveNotFound
	}
	s, ok := d.Live.Get(sid)
	if !ok || s.ProjectID != projectID {
		return domain.LiveSession{}, errLiveNotFound
	}
	return s, nil
}

// deviceErr turns the hub's errors into words for the person driving the
// agent, matching the dashboard's own messages.
func deviceErr(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, live.ErrDeviceSilent):
		return errors.New("the device did not answer; it is probably in the background — open the app on the phone and try again")
	case errors.Is(err, live.ErrDeviceGone), errors.Is(err, live.ErrNoSuchSession):
		return errors.New("the device is no longer connected")
	default:
		return internal(ctx, err)
	}
}

type listLiveSessionsIn struct {
	ProjectID string `json:"project_id" jsonschema:"The project's UUID."`
}

// liveSessionItem never carries the pairing code: until a device claims it, a
// code on a screen is a credential.
type liveSessionItem struct {
	ID    string `json:"id"`
	State string `json:"state"`
	// Device is empty while the session is still waiting to be claimed.
	Device         string     `json:"device,omitempty"`
	AppVersion     string     `json:"app_version,omitempty"`
	InstallationID string     `json:"installation_id,omitempty"`
	UserID         string     `json:"user_id,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	LastSeen       *time.Time `json:"last_seen,omitempty"`
	Watchers       int        `json:"watchers"`
}

type listLiveSessionsOut struct {
	Sessions []liveSessionItem `json:"sessions"`
}

type tailLiveSessionIn struct {
	ProjectID     string `json:"project_id" jsonschema:"The project's UUID."`
	LiveSessionID string `json:"live_session_id" jsonschema:"The live session, from list_live_sessions."`
	AfterSeq      int64  `json:"after_seq,omitempty" jsonschema:"Return only events with seq greater than this. Pass the latest_seq from the previous call to poll incrementally. Default 0 returns everything still buffered."`
	MaxEvents     int    `json:"max_events,omitempty" jsonschema:"Most events to return, keeping the newest. Default 100, maximum 250."`
}

type tailLiveSessionOut struct {
	// Events is what the device streamed, oldest first, from an in-memory
	// ring of the most recent 512. Nothing here is stored anywhere.
	Events []domain.LiveEvent `json:"events"`
	// Missed is how many events fell off the ring before this read; the
	// stream has a hole there and honesty beats a tidy-looking timeline.
	Missed int64 `json:"missed,omitempty"`
	// Dropped is how many buffered events were cut to fit max_events.
	Dropped int `json:"dropped,omitempty"`
	// LatestSeq is the newest event returned; pass it back as after_seq to
	// read only what happens next.
	LatestSeq int64 `json:"latest_seq"`
	// Live is false when the session has ended; the buffer is still readable
	// for about a minute afterwards.
	Live bool `json:"live"`
}

func (d Deps) addLiveTools(s *mcp.Server) {
	addTool(s, &mcp.Tool{
		Name: "list_live_sessions",
		Description: "Devices streaming to this project right now. A live session is a phone with " +
			"the app open, paired to the dashboard; its logs arrive in real time and nothing is " +
			"stored. Feeds tail_live_session and the live_db_* tools.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listLiveSessionsIn) (*mcp.CallToolResult, listLiveSessionsOut, error) {
		var out listLiveSessionsOut
		projectID, err := d.requireProject(ctx, in.ProjectID)
		if err != nil {
			return nil, out, err
		}
		sessions := d.Live.List(projectID)
		out.Sessions = make([]liveSessionItem, 0, len(sessions))
		for _, s := range sessions {
			item := liveSessionItem{
				ID:        s.ID.String(),
				State:     string(s.State),
				CreatedAt: s.CreatedAt,
				Watchers:  s.Watchers,
			}
			if s.State == domain.LiveConnected {
				item.Device = s.Device.Label()
				item.AppVersion = s.Device.AppVersion
				item.InstallationID = s.Device.InstallationID
				item.UserID = s.Device.UserID
				seen := s.LastSeen
				item.LastSeen = &seen
			}
			out.Sessions = append(out.Sessions, item)
		}
		return nil, out, nil
	})

	addTool(s, &mcp.Tool{
		Name: "tail_live_session",
		Description: "Read a live session's recent events: the last 512 logs the device streamed, " +
			"oldest first. One-shot, not a stream — call again with after_seq set to the previous " +
			"latest_seq to read only what happened since. Each call briefly counts as a watcher " +
			"on the dashboard, so do not poll tighter than the question needs.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in tailLiveSessionIn) (*mcp.CallToolResult, tailLiveSessionOut, error) {
		var out tailLiveSessionOut
		session, err := d.requireLiveSession(ctx, in.ProjectID, in.LiveSessionID)
		if err != nil {
			return nil, out, err
		}

		// afterSeq 1 returns everything still in the ring: the hub reads
		// "afterSeq <= 0" as a brand-new watcher and hands back nothing,
		// which is right for a human joining a stream and wrong for a
		// one-shot reader who asked for the buffer.
		afterSeq := in.AfterSeq
		if afterSeq <= 0 {
			afterSeq = 1
		}

		sub, err := d.Live.Watch(session.ID, afterSeq)
		if err != nil {
			if errors.Is(err, live.ErrTooManyWatchers) {
				return nil, out, errors.New("this live session already has its maximum number of watchers; try again in a moment")
			}
			return nil, out, deviceErr(ctx, err)
		}
		// Cancel immediately: the backlog is the whole read, and holding the
		// subscription would occupy a real watcher slot and leak the channel.
		sub.Cancel()

		events := sub.Backlog
		out.Missed = sub.Missed
		out.LatestSeq = afterSeq
		if len(events) > 0 {
			out.LatestSeq = events[len(events)-1].Seq
		}
		if max := clampLimit(in.MaxEvents, 100, 250); len(events) > max {
			out.Dropped = len(events) - max
			events = events[len(events)-max:]
		}
		out.Events = events
		out.Live = session.State == domain.LiveConnected
		return nil, out, nil
	})
}
