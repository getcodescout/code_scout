package mcptools

import (
	"context"
	"time"

	"github.com/getcodescout/code_scout/internal/domain"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listSessionsIn struct {
	ProjectID      string `json:"project_id" jsonschema:"The project's UUID."`
	InstallationID string `json:"installation_id,omitempty" jsonschema:"Narrow to one device's launches, from list_devices."`
	Limit          int    `json:"limit,omitempty" jsonschema:"Most sessions to return, newest first. Default 25, maximum 100."`
}

type sessionItem struct {
	ID             string    `json:"id"`
	InstallationID *string   `json:"installation_id,omitempty"`
	UserID         *string   `json:"user_id,omitempty"`
	Device         string    `json:"device,omitempty"`
	OSName         *string   `json:"os_name,omitempty"`
	OSVersion      *string   `json:"os_version,omitempty"`
	AppVersion     *string   `json:"app_version,omitempty"`
	BuildNumber    *string   `json:"build_number,omitempty"`
	SDKVersion     *string   `json:"sdk_version,omitempty"`
	Traits         any       `json:"traits,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	LastSeenAt     time.Time `json:"last_seen_at"`
}

type sessionSummaryItem struct {
	sessionItem
	DurationMS int64 `json:"duration_ms"`
	Logs       int64 `json:"logs"`
	Errors     int64 `json:"errors"`
	Network    int64 `json:"network_calls"`
}

type listSessionsOut struct {
	Sessions []sessionSummaryItem `json:"sessions"`
}

type sessionTimelineIn struct {
	ProjectID string `json:"project_id" jsonschema:"The project's UUID."`
	SessionID string `json:"session_id" jsonschema:"The launch to read, from list_sessions, a log's session_id, or a bug report copied from the app's overlay."`
	MaxLogs   int    `json:"max_logs,omitempty" jsonschema:"Most logs to return. Default 200, maximum 500. When the launch produced more, the newest are kept and capped is true."`
}

type sessionTimelineOut struct {
	Session sessionItem `json:"session"`
	// Logs is the launch in the order it happened, oldest first.
	Logs []toolLog `json:"logs"`
	// Capped true means the start of the launch was dropped to fit max_logs;
	// the newest logs are kept because the failure is usually at the end.
	Capped bool `json:"capped,omitempty"`
}

type listDevicesIn struct {
	ProjectID string `json:"project_id" jsonschema:"The project's UUID."`
	Limit     int    `json:"limit,omitempty" jsonschema:"Most devices to return, most recently seen first. Default 25, maximum 100."`
}

type deviceItem struct {
	InstallationID string    `json:"installation_id"`
	Device         string    `json:"device,omitempty"`
	OSName         *string   `json:"os_name,omitempty"`
	OSVersion      *string   `json:"os_version,omitempty"`
	AppVersion     *string   `json:"app_version,omitempty"`
	BuildNumber    *string   `json:"build_number,omitempty"`
	LastUserID     *string   `json:"last_user_id,omitempty"`
	Sessions       int64     `json:"sessions"`
	Logs           int64     `json:"logs"`
	Errors         int64     `json:"errors"`
	FirstSeen      time.Time `json:"first_seen"`
	LastSeen       time.Time `json:"last_seen"`
}

type listDevicesOut struct {
	Devices []deviceItem `json:"devices"`
}

type getDeviceIn struct {
	ProjectID      string `json:"project_id" jsonschema:"The project's UUID."`
	InstallationID string `json:"installation_id" jsonschema:"The device's installation id, from list_devices or a session."`
}

type getDeviceOut struct {
	Device deviceItem `json:"device"`
}

func sessionToItem(s domain.Session) sessionItem {
	item := sessionItem{
		ID:          s.ID.String(),
		UserID:      s.UserID,
		Device:      s.DeviceLabel(),
		OSName:      s.OSName,
		OSVersion:   s.OSVersion,
		AppVersion:  s.AppVersion,
		BuildNumber: s.BuildNumber,
		SDKVersion:  s.SDKVersion,
		StartedAt:   s.StartedAt,
		LastSeenAt:  s.LastSeenAt,
	}
	if s.InstallationID != nil {
		iid := s.InstallationID.String()
		item.InstallationID = &iid
	}
	item.Traits = rawJSON(s.Metadata)
	return item
}

func deviceToItem(dev domain.Device) deviceItem {
	return deviceItem{
		InstallationID: dev.InstallationID.String(),
		Device:         dev.DeviceLabel(),
		OSName:         dev.OSName,
		OSVersion:      dev.OSVersion,
		AppVersion:     dev.AppVersion,
		BuildNumber:    dev.BuildNumber,
		LastUserID:     dev.LastUserID,
		Sessions:       dev.Sessions,
		Logs:           dev.Logs,
		Errors:         dev.Errors,
		FirstSeen:      dev.FirstSeen,
		LastSeen:       dev.LastSeen,
	}
}

var (
	errSessionNotFound = errNotFoundFor("session")
	errDeviceNotFound  = errNotFoundFor("device")
)

func (d Deps) addSessionTools(s *mcp.Server) {
	addTool(s, &mcp.Tool{
		Name: "list_sessions",
		Description: "One row per app launch, newest first: device, OS, app version, the user if " +
			"the app named one, and how many logs, errors and network calls the launch produced.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listSessionsIn) (*mcp.CallToolResult, listSessionsOut, error) {
		var out listSessionsOut
		projectID, err := d.requireProject(ctx, in.ProjectID)
		if err != nil {
			return nil, out, err
		}

		var installationID *uuid.UUID
		if in.InstallationID != "" {
			iid, err := uuid.Parse(in.InstallationID)
			if err != nil {
				return nil, out, errDeviceNotFound
			}
			installationID = &iid
		}

		sessions, err := d.Logs.ListSessions(ctx, projectID, installationID, clampLimit(in.Limit, 25, 100))
		if err != nil {
			return nil, out, internal(ctx, err)
		}
		out.Sessions = make([]sessionSummaryItem, 0, len(sessions))
		for _, s := range sessions {
			out.Sessions = append(out.Sessions, sessionSummaryItem{
				sessionItem: sessionToItem(s.Session),
				DurationMS:  s.Duration().Milliseconds(),
				Logs:        s.Logs,
				Errors:      s.Errors,
				Network:     s.Network,
			})
		}
		return nil, out, nil
	})

	addTool(s, &mcp.Tool{
		Name: "get_session_timeline",
		Description: "Everything one launch did, in order: the session's device and app facts, then " +
			"its logs oldest first. This is the tool for a session id out of a bug report.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sessionTimelineIn) (*mcp.CallToolResult, sessionTimelineOut, error) {
		var out sessionTimelineOut
		projectID, err := d.requireProject(ctx, in.ProjectID)
		if err != nil {
			return nil, out, err
		}
		sessionID, err := uuid.Parse(in.SessionID)
		if err != nil {
			return nil, out, errSessionNotFound
		}

		session, err := d.Logs.GetSession(ctx, projectID, sessionID)
		if err != nil {
			if err == domain.ErrNotFound {
				return nil, out, errSessionNotFound
			}
			return nil, out, internal(ctx, err)
		}
		logs, err := d.Logs.GetSessionTimeline(ctx, projectID, sessionID)
		if err != nil {
			return nil, out, internal(ctx, err)
		}

		max := clampLimit(in.MaxLogs, 200, 500)
		if len(logs) > max {
			// Keep the end: a launch's failure is almost always its last act.
			logs = logs[len(logs)-max:]
			out.Capped = true
		}

		out.Session = sessionToItem(*session)
		out.Logs = make([]toolLog, 0, len(logs))
		for _, l := range logs {
			out.Logs = append(out.Logs, listLog(l))
		}
		return nil, out, nil
	})

	addTool(s, &mcp.Tool{
		Name: "list_devices",
		Description: "Launches rolled up by installation: one row per device install, with its " +
			"current model, OS and app version, and lifetime counts.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listDevicesIn) (*mcp.CallToolResult, listDevicesOut, error) {
		var out listDevicesOut
		projectID, err := d.requireProject(ctx, in.ProjectID)
		if err != nil {
			return nil, out, err
		}
		devices, err := d.Logs.ListDevices(ctx, projectID, clampLimit(in.Limit, 25, 100))
		if err != nil {
			return nil, out, internal(ctx, err)
		}
		out.Devices = make([]deviceItem, 0, len(devices))
		for _, dev := range devices {
			out.Devices = append(out.Devices, deviceToItem(dev))
		}
		return nil, out, nil
	})

	addTool(s, &mcp.Tool{
		Name:        "get_device",
		Description: "One device install by installation id, with its lifetime counts.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getDeviceIn) (*mcp.CallToolResult, getDeviceOut, error) {
		var out getDeviceOut
		projectID, err := d.requireProject(ctx, in.ProjectID)
		if err != nil {
			return nil, out, err
		}
		iid, err := uuid.Parse(in.InstallationID)
		if err != nil {
			return nil, out, errDeviceNotFound
		}
		dev, err := d.Logs.GetDevice(ctx, projectID, iid)
		if err != nil {
			if err == domain.ErrNotFound {
				return nil, out, errDeviceNotFound
			}
			return nil, out, internal(ctx, err)
		}
		out.Device = deviceToItem(*dev)
		return nil, out, nil
	})
}
