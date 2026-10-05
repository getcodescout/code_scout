package services

import (
	"archive/tar"
	"bytes"
	"context"
	"testing"

	"github.com/getcodescout/code_scout/internal/domain"
	"github.com/getcodescout/code_scout/internal/ports"
	"github.com/google/uuid"
)

// captureLogRepo keeps what ingest would have written.
type captureLogRepo struct {
	ports.LogRepository
	written []domain.Log
}

func (c *captureLogRepo) CreateBatch(_ context.Context, logs []domain.Log) (int64, error) {
	c.written = append(c.written, logs...)
	return int64(len(logs)), nil
}

type directTx struct{}

func (directTx) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func dataArchive(t *testing.T, dataJSON string) *tar.Reader {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	if err := w.WriteHeader(&tar.Header{Name: "data.json", Mode: 0o644, Size: int64(len(dataJSON)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := w.Write([]byte(dataJSON)); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	return tar.NewReader(&buf)
}

// method, url and status_code are promoted to columns for network logs only.
// A log the app wrote about a call can carry the same keys in its own
// metadata, and promoted there its status_code would count toward the
// overview's failed calls, a figure that filters on status_code alone.
func TestIngestPromotesCallColumnsForNetworkLogsOnly(t *testing.T) {
	rid := uuid.New()
	payload := `[
		{"id":"` + uuid.NewString() + `","session_id":"` + uuid.NewString() + `","level":"debug",
		 "message":"Network response","timestamp":"2026-09-23T10:00:00Z",
		 "is_network_call":1,"request_id":"` + rid.String() + `","call_phase":"response",
		 "metadata":{"status_code":200,"request":{"method":"GET","url":"https://api.test.dev/v2/cart"}}},
		{"id":"` + uuid.NewString() + `","session_id":"` + uuid.NewString() + `","level":"error",
		 "message":"Could not read GET /v2/cart","error":"type 'int' is not a subtype of type 'double' in type cast",
		 "timestamp":"2026-09-23T10:00:01Z",
		 "is_network_call":0,"request_id":"` + rid.String() + `","call_phase":null,
		 "metadata":{"status_code":500,"method":"GET","url":"https://api.test.dev/v2/cart"}}
	]`

	repo := &captureLogRepo{}
	svc := NewLogService(repo, directTx{}, nil, nil, nil, nil)
	if status, err := svc.DumpLogs(context.Background(), &domain.Project{ID: uuid.New()}, dataArchive(t, payload)); err != nil || status != 200 {
		t.Fatalf("ingest refused the batch: %d %v", status, err)
	}
	if len(repo.written) != 2 {
		t.Fatalf("want both logs written, got %d", len(repo.written))
	}
	phase, app := repo.written[0], repo.written[1]

	if phase.StatusCode == nil || *phase.StatusCode != 200 || phase.Method == nil || *phase.Method != "GET" {
		t.Errorf("the response phase lost its promoted columns: %+v", phase)
	}

	if app.IsNetworkCall || app.CallPhase != nil {
		t.Errorf("the app's log became a phase: network=%v phase=%v", app.IsNetworkCall, app.CallPhase)
	}
	if app.RequestID == nil || *app.RequestID != rid {
		t.Errorf("the app's log lost its request id: %v", app.RequestID)
	}
	if app.Method != nil || app.URL != nil || app.StatusCode != nil {
		t.Errorf("call columns were promoted onto the app's log: method=%v url=%v status=%v", app.Method, app.URL, app.StatusCode)
	}
	if app.Fingerprint == nil || *app.Fingerprint != domain.Fingerprint("error", app.Message, false, nil, nil) {
		t.Errorf("the app's log should group on its message like any app error, got %v", app.Fingerprint)
	}
}
