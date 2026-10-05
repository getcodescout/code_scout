package domain

import (
	"testing"

	"github.com/google/uuid"
)

// A log the app wrote about a call carries the call's request id and no phase.
// Every other field is a worse test than is_network_call: the request id is on
// both kinds, and a client could send a phase on a log that is not a call.
func TestSplitCallLogsGoesByIsNetworkCallAlone(t *testing.T) {
	rid := uuid.New()
	request, response, errPhase := CallPhaseRequest, CallPhaseResponse, CallPhaseError

	logs := []Log{
		{Message: "Network request", IsNetworkCall: true, RequestID: &rid, CallPhase: &request},
		{Message: "Network response", IsNetworkCall: true, RequestID: &rid, CallPhase: &response},
		{Message: "Could not read GET /v2/cart", Level: "error", RequestID: &rid},
		// A phase on a log that is not a call is still the app's log.
		{Message: "claims a phase", RequestID: &rid, CallPhase: &errPhase},
	}

	phases, linked := SplitCallLogs(logs)

	if len(phases) != 2 || phases[0].Message != "Network request" || phases[1].Message != "Network response" {
		t.Errorf("the phases should be the two network logs in order, got %+v", phases)
	}
	if len(linked) != 2 || linked[0].Message != "Could not read GET /v2/cart" || linked[1].Message != "claims a phase" {
		t.Errorf("the linked logs should be the two app logs in order, got %+v", linked)
	}
}
