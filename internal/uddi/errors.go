package uddi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// ErrNotFound is returned when a looked-up object does not exist.
var ErrNotFound = errors.New("not found")

// Error is an API call failure annotated with enough context to decide
// whether a retry is worthwhile.
type Error struct {
	// Op names the failed operation, e.g. "ListZones".
	Op string
	// StatusCode is the HTTP status, or 0 when no response was received.
	StatusCode int
	// Err is the underlying error.
	Err error
}

func (e *Error) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("uddi %s: http %d: %v", e.Op, e.StatusCode, e.Err)
	}
	return fmt.Sprintf("uddi %s: %v", e.Op, e.Err)
}

// Unwrap returns the underlying error.
func (e *Error) Unwrap() error { return e.Err }

// Retryable reports whether the failure is transient. Network errors, 5xx
// responses and 429 are retryable; other 4xx responses are not.
func (e *Error) Retryable() bool {
	if errors.Is(e.Err, context.Canceled) {
		return false
	}
	switch {
	case e.StatusCode == 0:
		return true
	case e.StatusCode == http.StatusTooManyRequests:
		return true
	case e.StatusCode >= 500:
		return true
	}
	return false
}

// IsRetryable reports whether err (or anything it wraps) is a retryable
// *Error. Unknown errors are treated as retryable so callers fail open.
func IsRetryable(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Retryable()
	}
	return !errors.Is(err, context.Canceled)
}

// wrap classifies an SDK failure into *Error using the response status.
func wrap(op string, resp *http.Response, err error) error {
	if err == nil {
		return nil
	}
	e := &Error{Op: op, Err: err}
	if resp != nil {
		e.StatusCode = resp.StatusCode
	}
	return e
}
