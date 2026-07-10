package anthropic

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

// APIStatusError is a condensed view of an Anthropic API status error. The
// SDK's error string leads with the request method, full URL, and the raw
// response JSON — noise that buries the actual cause ("prompt is too long:
// 435671 tokens > 200000 maximum") past where log panes and error chains
// truncate. This type puts the API's own message first and keeps the status
// code, error type, and request ID for correlation with the query ledger.
// Unwrap returns the SDK error so errors.As-based callers still work.
type APIStatusError struct {
	StatusCode int
	ErrType    string // API error type, e.g. "invalid_request_error"
	Message    string // API error message, e.g. "prompt is too long: ..."
	RequestID  string
	wrapped    error
}

func (e *APIStatusError) Error() string {
	errType := e.ErrType
	if errType == "" {
		errType = http.StatusText(e.StatusCode)
	}
	msg := fmt.Sprintf("anthropic API error (HTTP %d %s): %s", e.StatusCode, errType, e.Message)
	if e.RequestID != "" {
		msg += fmt.Sprintf(" [request_id %s]", e.RequestID)
	}
	return msg
}

func (e *APIStatusError) Unwrap() error { return e.wrapped }

// Permanent reports whether the failure cannot succeed on retry: client-side
// request errors (4xx) other than 408 (timeout) and 429 (rate limit) are
// permanent — most notably 400 invalid_request_error from an over-window
// prompt. 5xx and overload errors stay retryable.
func (e *APIStatusError) Permanent() bool {
	return e.StatusCode >= 400 && e.StatusCode < 500 &&
		e.StatusCode != http.StatusRequestTimeout && e.StatusCode != http.StatusTooManyRequests
}

// condenseAPIError rewrites an SDK API status error into an APIStatusError so
// the API's own message leads the error text. Non-API errors (network,
// context cancellation, ...) are returned unchanged.
func condenseAPIError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *sdk.Error
	if !errors.As(err, &apiErr) {
		return err
	}

	// The response body shape is {"type":"error","error":{"type":...,"message":...}}.
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	message := ""
	errType := ""
	if raw := apiErr.RawJSON(); raw != "" {
		if jsonErr := json.Unmarshal([]byte(raw), &body); jsonErr == nil {
			errType = body.Error.Type
			message = body.Error.Message
		}
	}
	if message == "" {
		// No parseable body — fall back to the SDK's full text so nothing is lost.
		message = apiErr.Error()
	}

	return &APIStatusError{
		StatusCode: apiErr.StatusCode,
		ErrType:    errType,
		Message:    message,
		RequestID:  apiErr.RequestID,
		wrapped:    apiErr,
	}
}
