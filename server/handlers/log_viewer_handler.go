package handlers

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/getcodescout/code_scout/internal/domain"
	"github.com/getcodescout/code_scout/internal/ports"
	"github.com/getcodescout/code_scout/internal/services"
	"github.com/getcodescout/code_scout/pkg/cslog"
	"github.com/getcodescout/code_scout/pkg/search"
	"github.com/getcodescout/code_scout/pkg/sse"
	"github.com/getcodescout/code_scout/server/middleware"
	"github.com/getcodescout/code_scout/view"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type LogViewerHandler struct {
	querySvc   *services.LogQueryService
	projectSvc ports.ProjectManager
	broker     *sse.Broker
	// settingsSvc supplies retention, which decides which overview ranges are
	// honest to offer. Read live like every other instance setting, so lowering
	// retention takes a range away on the next request rather than the next
	// restart.
	settingsSvc *services.InstanceSettingsService
}

func NewLogViewerHandler(querySvc *services.LogQueryService, projectSvc ports.ProjectManager, broker *sse.Broker, settingsSvc *services.InstanceSettingsService) *LogViewerHandler {
	return &LogViewerHandler{querySvc: querySvc, projectSvc: projectSvc, broker: broker, settingsSvc: settingsSvc}
}

// retentionDays is how far back this instance still holds logs.
//
// Settings fail open, so an unloaded settings service reports the default
// rather than zero. Zero would strip the overview down to its shortest range on
// an instance that is actually keeping a month, which is the wrong way to be
// wrong: it hides data that exists.
func (h *LogViewerHandler) retentionDays() int {
	if h.settingsSvc == nil || !h.settingsSvc.Loaded() {
		return domain.DefaultRetentionDays
	}
	return h.settingsSvc.Current().RetentionDays
}

// project loads the record the shell's sidebar needs. A nil result is not fatal
// to rendering: the sidebar falls back to the project id.
func (h *LogViewerHandler) project(ctx context.Context, projectID uuid.UUID) *domain.Project {
	project, _, err := h.projectSvc.GetProject(ctx, projectID)
	if err != nil {
		cslog.L(ctx).WithError(err).Warn("Could not load project for the sidebar")
		return nil
	}
	return project
}

// parseLogCursor reads the keyset cursor from query params. Both parts must be
// present and valid; otherwise pagination starts from the top.
func parseLogCursor(r *http.Request) *domain.LogCursor {
	ts := r.URL.Query().Get("cursor")
	idStr := r.URL.Query().Get("cursor_id")
	if ts == "" || idStr == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return nil
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return nil
	}
	return &domain.LogCursor{Time: t, ID: id}
}

// LogViewer renders the main log viewer page for a project.
// Overview handles GET /project/{id}/overview — the screen you land on.
func (h *LogViewerHandler) Overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	projectID, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		http.Error(w, "Invalid project ID", http.StatusBadRequest)
		return
	}

	project, _, err := h.projectSvc.GetProject(ctx, projectID)
	if err != nil {
		http.Error(w, "Project not found", http.StatusNotFound)
		return
	}

	// The range, out of the query string so the view you are looking at is a
	// link you can send. Clamped to what retention can honestly show, and an
	// unknown or now-unavailable key falls back to the default rather than
	// erroring: it arrives from a URL anyone can edit.
	keep := h.retentionDays()
	window := domain.OverviewWindowFor(r.URL.Query().Get("range"), keep)

	// A failed rollup renders the empty state rather than an error page: the
	// overview is a summary, and losing it should not lose the whole shell.
	stats, err := h.querySvc.GetProjectOverview(ctx, projectID, window)
	if err != nil {
		cslog.L(ctx).WithError(err).Error("Failed to load project overview")
	}

	recent, err := h.querySvc.GetErrorGroups(ctx, projectID, nil, 4)
	if err != nil {
		cslog.L(ctx).WithError(err).Error("Failed to load recent errors")
	}

	view.OverviewPage(view.OverviewData{
		User:         middleware.UserFrom(ctx),
		Project:      project,
		ProjectID:    projectID,
		Stats:        stats,
		RecentErrors: recent,
		Window:       window,
		Windows:      domain.OverviewWindowsFor(keep),
		// Whether the delta on the tiles means anything. The window fitting
		// inside retention is not enough: the comparison reaches back another
		// whole window, and on the default 30 days "Last 30 days" would report
		// every log as an increase over a month that was deleted.
		ShowDelta: window.ComparableAgainstPrevious(keep),
	}).Render(ctx, w)
}

// Errors handles GET /project/{id}/errors — one row per distinct problem.
func (h *LogViewerHandler) Errors(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	projectID, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		http.Error(w, "Invalid project ID", http.StatusBadRequest)
		return
	}

	// No window: an error that stopped happening yesterday is still the thing
	// you came here to find, and the row says when it was last seen.
	groups, err := h.querySvc.GetErrorGroups(ctx, projectID, nil, 100)
	if err != nil {
		cslog.L(ctx).WithError(err).Error("Failed to load error groups")
		http.Error(w, "Could not load errors", http.StatusInternalServerError)
		return
	}

	view.ErrorsPage(view.ErrorsData{
		User:      middleware.UserFrom(ctx),
		Project:   h.project(ctx, projectID),
		ProjectID: projectID,
		Groups:    groups,
	}).Render(ctx, w)
}

func (h *LogViewerHandler) LogViewer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vars := mux.Vars(r)

	projectID, err := uuid.Parse(vars["id"])
	if err != nil {
		http.Error(w, "Invalid project ID", http.StatusBadRequest)
		return
	}

	query := r.URL.Query().Get("q")
	cursor := parseLogCursor(r)

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	// A typo in the search box used to answer 400, which threw away the whole
	// screen — including the box you would fix it in. The complaint is shown
	// inline instead and the unfiltered list stays underneath.
	var queryError string
	filter, parseErr := search.Parse(query)
	if parseErr != nil {
		queryError = parseErr.Error()
		filter = &domain.SearchFilter{}
	}

	result, err := h.querySvc.ListLogs(ctx, projectID, effectiveQuery(query, queryError), cursor, limit)
	if err != nil {
		cslog.L(ctx).WithError(err).Error("Failed to list logs")
		http.Error(w, "Could not load logs", http.StatusInternalServerError)
		return
	}

	// Counted inside the same window the list uses, so a chip never offers a
	// tag that the current view has none of.
	tags, tagErr := h.querySvc.GetTagCounts(ctx, projectID, filter.Since, 200)
	if tagErr != nil {
		cslog.L(ctx).WithError(tagErr).Error("Failed to load tag counts")
	}

	data := view.LogViewerData{
		User:       middleware.UserFrom(ctx),
		Project:    h.project(ctx, projectID),
		ProjectID:  projectID,
		Logs:       result,
		Query:      query,
		Filter:     *filter,
		Tags:       tags,
		QueryError: queryError,
	}
	c := view.LogViewerPage(data)
	c.Render(ctx, w)
}

// effectiveQuery drops a query the parser rejected, so the page can show the
// broken text in the box while listing as though nothing were filtered.
func effectiveQuery(query, queryError string) string {
	if queryError != "" {
		return ""
	}
	return query
}

// LogsPartial returns just the log rows for HTMX infinite scroll.
func (h *LogViewerHandler) LogsPartial(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vars := mux.Vars(r)

	projectID, err := uuid.Parse(vars["id"])
	if err != nil {
		http.Error(w, "Invalid project ID", http.StatusBadRequest)
		return
	}

	query := r.URL.Query().Get("q")
	cursor := parseLogCursor(r)

	result, err := h.querySvc.ListLogs(ctx, projectID, query, cursor, 50)
	if err != nil {
		cslog.L(ctx).WithError(err).Error("Failed to list logs partial")
		http.Error(w, "Query failed", http.StatusInternalServerError)
		return
	}

	c := view.LogRows(result, projectID, query)
	c.Render(ctx, w)
}

// parseNetworkFilter reads the toolbar out of the query string.
func parseNetworkFilter(r *http.Request) domain.NetworkFilter {
	q := r.URL.Query()
	f := domain.NetworkFilter{
		Path:   strings.TrimSpace(q.Get("path")),
		Method: strings.ToUpper(strings.TrimSpace(q.Get("method"))),
		Status: strings.TrimSpace(q.Get("status")),
	}
	// An unknown value is dropped rather than passed to the query. These come
	// from a select, so anything else was typed into the URL by hand.
	if !validOption(f.Method, domain.HTTPMethods) {
		f.Method = ""
	}
	if !validOption(f.Status, domain.StatusClasses) {
		f.Status = ""
	}
	if sid, err := uuid.Parse(q.Get("session")); err == nil {
		f.SessionID = &sid
	}
	return f
}

func validOption(value string, allowed []string) bool {
	if value == "" {
		return true
	}
	for _, a := range allowed {
		if a == value {
			return true
		}
	}
	return false
}

// A project's call list is a window on something unbounded, so it is capped at
// what anyone scrolls. A launch is bounded, and the session screen shows all of
// it — past this many calls it is a launch nobody reads to the end of, and the
// Network screen filtered to the session is where to go through them all.
const (
	networkCallLimit = 200
	sessionCallLimit = 500
)

// networkView assembles a call list and whichever call is selected. The Network
// screen, the inspector fragment and the session screen's Network tab all
// render from it, so none of the three can disagree about what is selected.
func (h *LogViewerHandler) networkView(r *http.Request, projectID uuid.UUID, filter domain.NetworkFilter) (view.NetworkData, error) {
	ctx := r.Context()

	limit := networkCallLimit
	if filter.SessionID != nil {
		limit = sessionCallLimit
	}

	calls, err := h.querySvc.ListNetworkCalls(ctx, projectID, filter, limit)
	if err != nil {
		return view.NetworkData{}, err
	}

	data := view.NetworkData{
		User:      middleware.UserFrom(ctx),
		ProjectID: projectID,
		Calls:     calls,
		Filter:    filter,
	}

	// The selection: whatever rid names, or the first call so the pane is never
	// empty on arrival.
	selected := selectedCall(calls, r.URL.Query())
	if selected == nil {
		h.uncapturedCall(r, projectID, &data)
		return data, nil
	}

	phases, linkedTotal, err := h.querySvc.GetNetworkRequest(ctx, projectID, selected.RequestID, view.LinkedLogLimit)
	if err != nil {
		cslog.L(ctx).WithError(err).Error("Failed to load network phases")
		return data, nil
	}

	data.Selected = selected
	data.Phases = phases
	data.LinkedTotal = linkedTotal
	// `phase`, not `tab`: the session screen spends `tab` on Logs and Network,
	// and this pane has to work there too.
	data.Phase = r.URL.Query().Get("phase")
	if !view.TabAvailable(phases, data.Phase) {
		data.Phase = view.DefaultTab(phases)
	}
	return data, nil
}

// uncapturedCall answers an rid with no row in the list whose only logs are
// the app's own: the SDK dropped its phases at the level gate, or retention
// removed them. An rid the list left out for any other reason, a filter or the
// cap, still selects nothing.
//
// A launch is a filter too. Its screen shows a call that was not captured only
// when the app logged about it in that launch, or the pane would list another
// launch's logs under this one's header.
func (h *LogViewerHandler) uncapturedCall(r *http.Request, projectID uuid.UUID, data *view.NetworkData) {
	rid, err := uuid.Parse(r.URL.Query().Get("rid"))
	if err != nil {
		return
	}
	logs, linkedTotal, err := h.querySvc.GetNetworkRequest(r.Context(), projectID, rid, view.LinkedLogLimit)
	if err != nil {
		cslog.L(r.Context()).WithError(err).Error("Failed to load the logs for an unlisted request")
		return
	}
	phases, linked := domain.SplitCallLogs(logs)
	if len(phases) > 0 || linkedTotal == 0 {
		return
	}
	if sid := data.Filter.SessionID; sid != nil && !anyInLaunch(linked, *sid) {
		return
	}
	data.Uncaptured = &rid
	data.Phases = logs
	data.LinkedTotal = linkedTotal
}

func anyInLaunch(logs []domain.Log, sessionID uuid.UUID) bool {
	for _, l := range logs {
		if l.SessionID == sessionID {
			return true
		}
	}
	return false
}

// selectedCall reads three different intents out of one parameter. No rid at
// all is "no opinion", and the newest call opens. An empty rid= is the
// inspector deliberately closed, which has to survive a reload. Anything else
// names a call.
func selectedCall(calls []domain.NetworkCall, q url.Values) *domain.NetworkCall {
	if len(calls) == 0 {
		return nil
	}
	rid := q.Get("rid")
	if q.Has("rid") && rid == "" {
		return nil
	}
	if id, err := uuid.Parse(rid); err == nil {
		for i := range calls {
			if calls[i].RequestID == id {
				return &calls[i]
			}
		}
		// An rid that is not in the filtered list selects nothing rather than
		// silently showing a different call.
		return nil
	}
	return &calls[0]
}

// Network handles GET /project/{id}/network — one row per call.
func (h *LogViewerHandler) Network(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	projectID, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		http.Error(w, "Invalid project ID", http.StatusBadRequest)
		return
	}

	data, err := h.networkView(r, projectID, parseNetworkFilter(r))
	if err != nil {
		cslog.L(ctx).WithError(err).Error("Failed to list network calls")
		http.Error(w, "Could not load network calls", http.StatusInternalServerError)
		return
	}
	data.Project = h.project(ctx, projectID)

	view.NetworkPage(data).Render(ctx, w)
}

// NetworkInspector answers a row click with just the detail pane.
func (h *LogViewerHandler) NetworkInspector(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	projectID, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		http.Error(w, "Invalid project ID", http.StatusBadRequest)
		return
	}

	filter := parseNetworkFilter(r)
	data, err := h.networkView(r, projectID, filter)
	if err != nil {
		cslog.L(ctx).WithError(err).Error("Failed to load the network inspector")
		http.Error(w, "Could not load the call", http.StatusInternalServerError)
		return
	}

	// A launch-scoped list counts "since launch" from the session's own start,
	// and the rows going back out of band have that column. The session screen
	// loaded the row to draw its header; this endpoint has to ask for it.
	if filter.SessionID != nil {
		if session, err := h.querySvc.GetSession(ctx, projectID, *filter.SessionID); err == nil {
			data.SessionStart = session.StartedAt
		}
	}

	// Two things change on a row click: the pane, and which row is lit up. The
	// pane is what hx-target asked for; the rows ride along out of band. Both
	// go in one response so the list can never disagree with the pane.
	view.NetworkDetailPane(data).Render(ctx, w)
	view.NetworkRowsOOB(data).Render(ctx, w)
}

// Sessions handles GET /project/{id}/sessions — one row per app launch.
func (h *LogViewerHandler) Sessions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	projectID, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		http.Error(w, "Invalid project ID", http.StatusBadRequest)
		return
	}

	sessions, err := h.querySvc.ListSessions(ctx, projectID, nil, 200)
	if err != nil {
		cslog.L(ctx).WithError(err).Error("Failed to list sessions")
		http.Error(w, "Could not load sessions", http.StatusInternalServerError)
		return
	}

	// The header counts are a summary. Losing them should not lose the table.
	total, users, err := h.querySvc.SessionCounts(ctx, projectID)
	if err != nil {
		cslog.L(ctx).WithError(err).Error("Failed to count sessions")
	}

	view.SessionsPage(view.SessionsData{
		User:      middleware.UserFrom(ctx),
		Project:   h.project(ctx, projectID),
		ProjectID: projectID,
		Sessions:  sessions,
		Total:     total,
		Users:     users,
	}).Render(ctx, w)
}

// Devices handles GET /project/{id}/devices — one row per install.
func (h *LogViewerHandler) Devices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	projectID, err := uuid.Parse(mux.Vars(r)["id"])
	if err != nil {
		http.Error(w, "Invalid project ID", http.StatusBadRequest)
		return
	}

	devices, err := h.querySvc.ListDevices(ctx, projectID, 200)
	if err != nil {
		cslog.L(ctx).WithError(err).Error("Failed to list devices")
		http.Error(w, "Could not load devices", http.StatusInternalServerError)
		return
	}

	view.DevicesPage(view.DevicesData{
		User:      middleware.UserFrom(ctx),
		Project:   h.project(ctx, projectID),
		ProjectID: projectID,
		Devices:   devices,
	}).Render(ctx, w)
}

// DeviceDetail handles GET /project/{id}/device/{iid} — one install and every
// session it has run.
func (h *LogViewerHandler) DeviceDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vars := mux.Vars(r)

	projectID, err := uuid.Parse(vars["id"])
	if err != nil {
		http.Error(w, "Invalid project ID", http.StatusBadRequest)
		return
	}
	installationID, err := uuid.Parse(vars["iid"])
	if err != nil {
		http.Error(w, "Invalid installation ID", http.StatusBadRequest)
		return
	}

	device, err := h.querySvc.GetDevice(ctx, projectID, installationID)
	if err != nil {
		// Scoped to the project already, so a miss means it does not exist here.
		http.Error(w, "Device not found", http.StatusNotFound)
		return
	}

	sessions, err := h.querySvc.ListSessions(ctx, projectID, &installationID, 200)
	if err != nil {
		cslog.L(ctx).WithError(err).Error("Failed to list device sessions")
		http.Error(w, "Could not load sessions", http.StatusInternalServerError)
		return
	}

	view.DeviceDetailPage(view.DeviceDetailData{
		User:      middleware.UserFrom(ctx),
		Project:   h.project(ctx, projectID),
		ProjectID: projectID,
		Device:    *device,
		Sessions:  sessions,
	}).Render(ctx, w)
}

// SessionTimeline renders one app launch: its logs and its network calls.
func (h *LogViewerHandler) SessionTimeline(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vars := mux.Vars(r)

	projectID, err := uuid.Parse(vars["id"])
	if err != nil {
		http.Error(w, "Invalid project ID", http.StatusBadRequest)
		return
	}
	sessionID, err := uuid.Parse(vars["sid"])
	if err != nil {
		http.Error(w, "Invalid session ID", http.StatusBadRequest)
		return
	}

	logs, err := h.querySvc.GetSessionTimeline(ctx, projectID, sessionID)
	if err != nil {
		cslog.L(ctx).WithError(err).Error("Failed to get session timeline")
		http.Error(w, "Failed to load session", http.StatusInternalServerError)
		return
	}

	// Both panes are loaded whichever tab is showing, because the tab labels
	// carry counts and a count you cannot see until you click it is not a
	// count. Both queries are already scoped to one session, so this is two
	// indexed reads over one launch rather than anything the size of a project.
	//
	// The Network tab is the Network screen's split pane filtered to this
	// launch, so it is built by the same code, and a selected call arrives with
	// its phases already loaded — a shared link opens on the call it names.
	network, err := h.networkView(r, projectID, domain.NetworkFilter{SessionID: &sessionID})
	if err != nil {
		// The logs are the point of the screen; a network list that failed to
		// load should not take them down with it.
		cslog.L(ctx).WithError(err).Error("Failed to list a session's network calls")
	}

	// The session row may be missing: an SDK older than 1.2.0 uploaded logs
	// without ever sending one. The screen renders from the logs in that case
	// rather than 404ing on a launch whose events are right there.
	session, err := h.querySvc.GetSession(ctx, projectID, sessionID)
	if err != nil {
		cslog.L(ctx).WithError(err).Debug("No session row for this launch")
	}
	if session != nil {
		network.SessionStart = session.StartedAt
	}

	tab := r.URL.Query().Get("tab")
	if tab != "network" {
		tab = "logs"
	}

	data := view.SessionDetailData{
		User:      middleware.UserFrom(ctx),
		Project:   h.project(ctx, projectID),
		ProjectID: projectID,
		SessionID: sessionID,
		Session:   session,
		Tab:       tab,
		Logs:      logs,
		Network:   network,
	}
	view.SessionDetailPage(data).Render(ctx, w)
}

// NetworkDetail renders the network request detail page.
func (h *LogViewerHandler) NetworkDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vars := mux.Vars(r)

	projectID, err := uuid.Parse(vars["id"])
	if err != nil {
		http.Error(w, "Invalid project ID", http.StatusBadRequest)
		return
	}
	requestID, err := uuid.Parse(vars["rid"])
	if err != nil {
		http.Error(w, "Invalid request ID", http.StatusBadRequest)
		return
	}

	logs, linkedTotal, err := h.querySvc.GetNetworkRequest(ctx, projectID, requestID, view.LinkedLogLimit)
	if err != nil {
		cslog.L(ctx).WithError(err).Error("Failed to get network request")
		http.Error(w, "Failed to load request", http.StatusInternalServerError)
		return
	}

	data := view.NetworkDetailData{
		User:        middleware.UserFrom(ctx),
		Project:     h.project(ctx, projectID),
		ProjectID:   projectID,
		RequestID:   requestID,
		Logs:        logs,
		LinkedTotal: linkedTotal,
	}
	c := view.NetworkDetailPage(data)
	c.Render(ctx, w)
}

// StreamLogs handles the SSE endpoint for real-time log streaming.
func (h *LogViewerHandler) StreamLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := cslog.L(ctx)

	projectIDStr := r.URL.Query().Get("project_id")
	projectID, err := uuid.Parse(projectIDStr)
	if err != nil {
		http.Error(w, "Invalid project_id", http.StatusBadRequest)
		return
	}

	// Disable write timeout for SSE
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		log.WithError(err).Warn("Failed to disable write deadline for SSE")
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Subscribe to the broker
	ch, err := h.broker.Subscribe(projectID)
	if err != nil {
		log.WithError(err).Error("SSE: failed to subscribe")
		http.Error(w, "Too many viewers", http.StatusTooManyRequests)
		return
	}
	defer h.broker.Unsubscribe(projectID, ch)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Send initial connection event
	fmt.Fprintf(w, "event: connected\ndata: {\"project_id\":\"%s\"}\n\n", projectID)
	flusher.Flush()

	log.WithField("project_id", projectID).Info("SSE: client connected")

	for {
		select {
		case <-ctx.Done():
			log.Info("SSE: client disconnected")
			return
		case logs, ok := <-ch:
			if !ok {
				log.Info("SSE: channel closed")
				return
			}
			// Render each log as an HTML fragment via templ. SSE data fields
			// are line-delimited, so multi-line HTML must be re-framed with a
			// "data: " prefix per line or the first newline ends the event.
			for _, l := range logs {
				var buf bytes.Buffer
				if err := view.SSELogRow(l).Render(ctx, &buf); err != nil {
					log.WithError(err).Warn("SSE: failed to render log row")
					continue
				}
				fmt.Fprintf(w, "event: log\n")
				for _, line := range strings.Split(buf.String(), "\n") {
					fmt.Fprintf(w, "data: %s\n", line)
				}
				fmt.Fprintf(w, "\n")
			}
			flusher.Flush()
		}
	}
}

// ProjectStats returns sparkline stats as an HTMX partial.
func (h *LogViewerHandler) ProjectStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vars := mux.Vars(r)

	projectID, err := uuid.Parse(vars["id"])
	if err != nil {
		http.Error(w, "Invalid project ID", http.StatusBadRequest)
		return
	}

	stats, err := h.querySvc.GetLogStats(ctx, projectID, 24)
	if err != nil {
		cslog.L(ctx).WithError(err).Error("Failed to get stats")
		// Return empty sparkline on error
		w.WriteHeader(http.StatusOK)
		return
	}

	c := view.SparklineStats(stats)
	c.Render(ctx, w)
}
