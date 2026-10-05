package db

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"github.com/getcodescout/code_scout/internal/domain"
	"github.com/getcodescout/code_scout/pkg/cslog"
	"github.com/getcodescout/code_scout/server/middleware"
)

// A network call has a request id of its own, and the HTTP request that opens
// it has another. request_id on a server log line is the HTTP one, on every
// line the request produced, so that grepping it gives the whole request. The
// call's id goes under its own key, or the line that loads the call drops out
// of that grep.
//
// Through the real HttpLogger, so the request_id asserted on is the one the
// server actually puts on the context.
func TestLoadingACallKeepsTheHTTPRequestID(t *testing.T) {
	db := testDB(t)
	repo := NewLogRepo(db)
	projectID := seedProject(t, db)
	callID := uuid.New()

	logger := cslog.GetLogger()
	oldOut, oldLevel, oldFmt := logger.Out, logger.Level, logger.Formatter
	t.Cleanup(func() { logger.SetOutput(oldOut); logger.SetLevel(oldLevel); logger.SetFormatter(oldFmt) })

	var out bytes.Buffer
	logger.SetOutput(&out)
	logger.SetLevel(logrus.DebugLevel)
	logger.SetFormatter(&logrus.JSONFormatter{})

	if _, err := repo.CreateBatch(t.Context(), []domain.Log{
		netPhase(projectID, uuid.New(), callID, "request", "GET", "https://api.test/v2/cart", nil, time.Now().Add(-time.Minute)),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	out.Reset()

	open := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, err := repo.GetByRequestID(r.Context(), projectID, callID, 3); err != nil {
			t.Errorf("get: %v", err)
		}
	})
	middleware.HttpLogger(open).ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest("GET", "/project/"+projectID.String()+"/network/"+callID.String(), nil))

	lines := map[string]map[string]any{}
	for _, raw := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("log line is not json: %v\n%s", err, raw)
		}
		if msg, _ := line["msg"].(string); msg != "" {
			lines[msg] = line
		}
	}
	request, loaded := lines["Request"], lines["DB: GetByRequestID"]
	if request == nil || loaded == nil {
		t.Fatalf("want the request's line and the call's line, got:\n%s", out.String())
	}

	httpID, _ := request["request_id"].(string)
	if !strings.HasPrefix(httpID, "req-") {
		t.Fatalf("the request's own line has no HTTP request id: %v", request)
	}
	if got := loaded["request_id"]; got != httpID {
		t.Errorf("request_id on the call's line is %v, want the HTTP request's %s", got, httpID)
	}
	if got := loaded["call_request_id"]; got != callID.String() {
		t.Errorf("call_request_id on the call's line is %v, want the call's %s", got, callID)
	}
}
