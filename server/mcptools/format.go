package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/getcodescout/code_scout/internal/domain"
	"github.com/getcodescout/code_scout/pkg/cslog"
	"github.com/getcodescout/code_scout/pkg/search"
	"github.com/google/uuid"
)

// Budgets for list contexts. A single log can legally carry a 32 KB body in
// its metadata, and fifty of those in one tool result would drown the very
// context window the tool exists to inform. Lists omit or truncate with a
// flag; get_log returns a log whole, and get_network_request a call's phases.
const (
	// messageBudget is in runes so a cut cannot split a UTF-8 sequence.
	messageBudget = 2000
	// metadataBudget is in bytes of raw JSON. Oversize metadata is omitted
	// whole rather than truncated: cut JSON is not JSON, and a fragment that
	// almost parses is worse for an agent than a flag that says "fetch it".
	metadataBudget = 2048
	// stackBudget is larger because stack traces are the payload of the
	// errors workflow.
	stackBudget = 8192
)

// toolLog mirrors the export endpoint's snake_case log shape, plus the
// promoted network columns and honesty flags for anything a list omitted.
//
// The free-form fields are `any`, not json.RawMessage: an output schema is
// inferred from this struct and every result is validated against it, and
// RawMessage infers as an array of bytes while carrying an object, so every
// log with metadata would fail its own tool's validation.
type toolLog struct {
	ID               string  `json:"id"`
	SessionID        string  `json:"session_id"`
	Level            string  `json:"level"`
	Message          string  `json:"message"`
	MessageTruncated bool    `json:"message_truncated,omitempty"`
	Error            *string `json:"error,omitempty"`
	StackTrace       any     `json:"stack_trace,omitempty"`
	// StackTraceOmitted with stack_trace absent means it existed but was over
	// budget; fetch the log by id for the whole thing.
	StackTraceOmitted bool      `json:"stack_trace_omitted,omitempty"`
	Metadata          any       `json:"metadata,omitempty"`
	MetadataOmitted   bool      `json:"metadata_omitted,omitempty"`
	MetadataBytes     int       `json:"metadata_bytes,omitempty"`
	Tags              any       `json:"tags,omitempty"`
	Timestamp         time.Time `json:"timestamp"`
	IsNetworkCall     bool      `json:"is_network_call,omitempty"`
	RequestID         *string   `json:"request_id,omitempty"`
	CallPhase         *string   `json:"call_phase,omitempty"`
	Method            *string   `json:"method,omitempty"`
	URL               *string   `json:"url,omitempty"`
	StatusCode        *int      `json:"status_code,omitempty"`
}

// rawJSON decodes a stored jsonb value for a tool result. The stored bytes
// are valid JSON by construction; anything else decodes to nil and is
// omitted rather than sent broken.
//
// Numbers stay json.Number, so they are written back as the literal the app
// sent: 12.0 is not 12 to a model that wants a double, and an id past 2^53 is
// not an id once it has been through a float64.
//
// A number too large for any float64 is the exception. jsonb stores one, and
// addTool's schema check and any client reading into a float64 refuse it, so
// it would fail the whole result it is in, a page of search_logs included. The
// value holding it is omitted instead. The SDK never sends one.
func rawJSON(r *json.RawMessage) any {
	if r == nil || len(*r) == 0 {
		return nil
	}
	if err := json.Unmarshal(*r, new(any)); err != nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(*r))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil
	}
	return v
}

// listLog renders a log for a list context, with the budgets applied.
func listLog(l domain.Log) toolLog { return renderLog(l, true) }

// wholeLog renders a log with nothing omitted, for the directed fetches.
func wholeLog(l domain.Log) toolLog { return renderLog(l, false) }

func renderLog(l domain.Log, budgeted bool) toolLog {
	out := toolLog{
		ID:            l.ID.String(),
		SessionID:     l.SessionID.String(),
		Level:         l.Level,
		Message:       l.Message,
		Error:         l.Error,
		Timestamp:     l.TimeStamp,
		IsNetworkCall: l.IsNetworkCall,
		Method:        l.Method,
		URL:           l.URL,
		StatusCode:    l.StatusCode,
	}
	if l.RequestID != nil {
		s := l.RequestID.String()
		out.RequestID = &s
	}
	if l.CallPhase != nil {
		s := string(*l.CallPhase)
		out.CallPhase = &s
	}
	out.Tags = rawJSON(l.Tags)
	if budgeted {
		if runes := []rune(out.Message); len(runes) > messageBudget {
			out.Message = string(runes[:messageBudget])
			out.MessageTruncated = true
		}
	}
	if l.Metadata != nil {
		if budgeted && len(*l.Metadata) > metadataBudget {
			out.MetadataOmitted = true
			out.MetadataBytes = len(*l.Metadata)
		} else {
			out.Metadata = rawJSON(l.Metadata)
		}
	}
	if l.StackTrace != nil {
		if budgeted && len(*l.StackTrace) > stackBudget {
			out.StackTraceOmitted = true
		} else {
			out.StackTrace = rawJSON(l.StackTrace)
		}
	}
	return out
}

// encodeCursor writes a keyset cursor as "RFC3339Nano|uuid". Stateless and
// human-readable, so a mangled one is diagnosable by eye.
func encodeCursor(c *domain.LogCursor) string {
	if c == nil {
		return ""
	}
	return c.Time.Format(time.RFC3339Nano) + "|" + c.ID.String()
}

func decodeCursor(raw string) (*domain.LogCursor, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	at, id, found := strings.Cut(raw, "|")
	if !found {
		return nil, errBadCursor
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return nil, errBadCursor
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return nil, errBadCursor
	}
	return &domain.LogCursor{Time: t, ID: u}, nil
}

var errBadCursor = errors.New("invalid cursor; pass the next_cursor from a previous search_logs result, or omit it for the first page")

// clampLimit applies a tool's default and ceiling. Zero or negative means
// "the default"; anything above the ceiling is the ceiling rather than an
// error, because "give me everything" should mean "give me the most allowed".
func clampLimit(n, def, max int) int {
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// sinceHours maps an optional "how far back" input to the *time.Time the
// services take. Zero or negative means no bound.
func sinceHours(h int) *time.Time {
	if h <= 0 {
		return nil
	}
	t := time.Now().Add(-time.Duration(h) * time.Hour)
	return &t
}

// internal logs the real error against the request and hands the client a
// bare "internal error". The SDK packs a returned error's text into the tool
// result, so anything richer than this would carry internals to the client —
// sanitised at the source, where it cannot regress.
func internal(ctx context.Context, err error) error {
	cslog.L(ctx).WithError(err).Error("MCP tool failed")
	return errors.New("internal error")
}

// parseSearchErr surfaces a search syntax error with its position, and sends
// everything else through internal. The parse error's text is written for the
// person who typed the query; nothing in it is ours to hide.
func parseSearchErr(ctx context.Context, err error) error {
	var pe *search.ParseError
	if errors.As(err, &pe) {
		return fmt.Errorf("invalid search query: %s", pe.Error())
	}
	return internal(ctx, err)
}
