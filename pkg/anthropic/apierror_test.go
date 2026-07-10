package anthropic

import (
	"errors"
	"strings"
	"testing"
)

// TestAPIStatusErrorMessage covers the condensed error text: the API's own
// message must lead, with status/type/request-id present for ledger
// correlation and no URL/raw-JSON noise.
func TestAPIStatusErrorMessage(t *testing.T) {
	err := &APIStatusError{
		StatusCode: 400,
		ErrType:    "invalid_request_error",
		Message:    "prompt is too long: 435671 tokens > 200000 maximum",
		RequestID:  "req_test123",
	}
	msg := err.Error()
	if !strings.Contains(msg, "HTTP 400 invalid_request_error") {
		t.Errorf("missing status/type: %q", msg)
	}
	if !strings.Contains(msg, "prompt is too long: 435671 tokens > 200000 maximum") {
		t.Errorf("missing API message: %q", msg)
	}
	if !strings.Contains(msg, "req_test123") {
		t.Errorf("missing request id: %q", msg)
	}

	// Missing error type falls back to the HTTP status text.
	noType := &APIStatusError{StatusCode: 429, Message: "rate limited"}
	if !strings.Contains(noType.Error(), "HTTP 429 Too Many Requests") {
		t.Errorf("status-text fallback missing: %q", noType.Error())
	}
}

// TestAPIStatusErrorPermanent covers retryability classification by status.
func TestAPIStatusErrorPermanent(t *testing.T) {
	cases := []struct {
		status int
		want   bool
	}{
		{400, true}, // invalid_request_error — retries fail identically
		{403, true},
		{404, true},
		{408, false}, // request timeout — retryable
		{429, false}, // rate limit — retryable
		{500, false},
		{529, false}, // overloaded
	}
	for _, tc := range cases {
		e := &APIStatusError{StatusCode: tc.status}
		if got := e.Permanent(); got != tc.want {
			t.Errorf("Permanent() for %d = %v, want %v", tc.status, got, tc.want)
		}
	}
}

// TestCondenseAPIErrorPassthrough: non-API errors are returned unchanged so
// the stream-error wrapping still applies to transport failures.
func TestCondenseAPIErrorPassthrough(t *testing.T) {
	plain := errors.New("connection reset by peer")
	if got := condenseAPIError(plain); got != plain {
		t.Errorf("non-API error should pass through unchanged, got %v", got)
	}
	if condenseAPIError(nil) != nil {
		t.Error("nil should stay nil")
	}
}
