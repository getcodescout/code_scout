package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// A network call is not a row in the logs table — it is three of them.
//
// The SDK writes one log per phase (request, response, error), linked by
// request id. The log viewer shows those as they are, which is honest but hard
// to read: a failing endpoint appears as two unrelated lines. NetworkCall is
// the three phases collapsed back into the one call they describe.
type NetworkCall struct {
	RequestID uuid.UUID
	SessionID uuid.UUID

	Method     *string
	URL        *string
	StatusCode *int

	// StartedAt is the earliest phase and EndedAt the latest, so a call still
	// in flight has both set to the moment the request went out.
	StartedAt time.Time
	EndedAt   time.Time

	HasRequest  bool
	HasResponse bool
	HasError    bool

	// ErrorMessage is the error phase's message, which for the Flutter SDK is
	// always the literal "Network Error" — the useful detail is in its
	// metadata, which the inspector reads.
	ErrorMessage *string
}

// SplitCallLogs separates the logs that share one request id into the call's
// own phases and the logs the app wrote about it, such as a response body its
// model could not read.
//
// The test is is_network_call and nothing else, the one ListNetworkCalls pairs
// on. Sorting on a present request id, or on a missing phase, would count the
// app's log as part of the call.
func SplitCallLogs(logs []Log) (phases, linked []Log) {
	for _, l := range logs {
		if l.IsNetworkCall {
			phases = append(phases, l)
		} else {
			linked = append(linked, l)
		}
	}
	return phases, linked
}

// CallNotCaptured is what every surface says for a request id that the app
// logged against but whose phases were never stored.
const CallNotCaptured = "The request and response for this call were not stored. " +
	"The SDK writes network logs at debug, so an app whose minimumLevel is info or higher " +
	"keeps none of them. Retention can also remove them."

// CallState is the four outcomes the prototype asks to be distinguishable.
type CallState string

const (
	// CallFailed is an error phase: the request never completed.
	CallFailed CallState = "error"
	// CallPending is a request with nothing after it. Either still in flight,
	// or the app died before the response arrived — which is itself a finding.
	CallPending CallState = "pending"
	// CallComplete is a request and its response.
	CallComplete CallState = "complete"
	// CallOrphaned is a response with no request. It happens when a batch is
	// split across two uploads and the second lands first.
	CallOrphaned CallState = "response-only"
)

func (c NetworkCall) State() CallState {
	switch {
	case c.HasError:
		return CallFailed
	case c.HasResponse && !c.HasRequest:
		return CallOrphaned
	case c.HasResponse:
		return CallComplete
	default:
		return CallPending
	}
}

// Duration is how long the call took. Zero while pending: a call with only a
// request has no end yet, and timing it against "now" would make the number
// grow every time the page is refreshed.
func (c NetworkCall) Duration() time.Duration {
	if c.State() == CallPending {
		return 0
	}
	if c.EndedAt.Before(c.StartedAt) {
		return 0
	}
	return c.EndedAt.Sub(c.StartedAt)
}

// Path is the part of the URL worth putting in a table. The host is dropped
// because it is the same for every row, and the query string is kept because
// it is often the thing that differs.
func (c NetworkCall) Path() string {
	if c.URL == nil || *c.URL == "" {
		return "—"
	}
	s := *c.URL
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		if j := strings.Index(s, "/"); j >= 0 {
			s = s[j:]
		} else {
			s = "/"
		}
	}
	if s == "" {
		return "/"
	}
	return s
}

// Host is shown in the inspector, where there is room for it.
func (c NetworkCall) Host() string {
	if c.URL == nil {
		return ""
	}
	s := *c.URL
	i := strings.Index(s, "://")
	if i < 0 {
		return ""
	}
	s = s[i+3:]
	if j := strings.IndexAny(s, "/?#"); j >= 0 {
		s = s[:j]
	}
	return s
}

// StatusLabel is what goes in the status column: the code, or the word for a
// call that has no code to show.
func (c NetworkCall) StatusLabel() string {
	if c.StatusCode != nil {
		return itoa(*c.StatusCode)
	}
	switch c.State() {
	case CallFailed:
		return "error"
	case CallPending:
		return "pending"
	default:
		return "—"
	}
}

// Failed reports whether this call is worth someone's attention: a transport
// error, or any status from 400 up.
func (c NetworkCall) Failed() bool {
	if c.HasError {
		return true
	}
	return c.StatusCode != nil && *c.StatusCode >= 400
}

// NetworkFilter is the toolbar. Like the log viewer's, it lives entirely in the
// query string, so a filtered view is a link someone can send.
type NetworkFilter struct {
	// Path matches anywhere in the URL, so "checkout" finds both
	// /v2/checkout/session and /v2/cart/checkout.
	Path string
	// Method is exact and upper-case, or empty for any.
	Method string
	// Status is a class rather than a code: "2xx", "4xx", "5xx", or "failed"
	// which is everything from 400 up plus the transport errors. Empty for any.
	Status string
	// SessionID scopes the list to one launch, which is how the session
	// timeline links into this screen.
	SessionID *uuid.UUID
}

// HTTPMethods is the offered order in the method picker.
var HTTPMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}

// StatusClasses is the offered order in the status picker.
var StatusClasses = []string{"2xx", "3xx", "4xx", "5xx", "failed"}

// IsEmpty reports whether anything is being filtered.
func (f NetworkFilter) IsEmpty() bool {
	return f.Path == "" && f.Method == "" && f.Status == "" && f.SessionID == nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
