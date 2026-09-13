package uddi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestErrorRetryable(t *testing.T) {
	tests := []struct {
		name      string
		err       *Error
		retryable bool
		contains  string
	}{
		{"network", &Error{Op: "ListZones", Err: errors.New("dial tcp: refused")}, true, "uddi ListZones: dial tcp: refused"},
		{"500", &Error{Op: "ListZones", StatusCode: 500, Err: errors.New("boom")}, true, "http 500"},
		{"503", &Error{Op: "CreateRecord", StatusCode: 503, Err: errors.New("boom")}, true, "http 503"},
		{"429", &Error{Op: "CreateRecord", StatusCode: 429, Err: errors.New("slow down")}, true, "http 429"},
		{"400", &Error{Op: "CreateRecord", StatusCode: 400, Err: errors.New("bad")}, false, "http 400"},
		{"401", &Error{Op: "ResolveView", StatusCode: 401, Err: errors.New("unauth")}, false, "http 401"},
		{"404", &Error{Op: "DeleteRecord", StatusCode: 404, Err: ErrNotFound}, false, "not found"},
		{"cancelled", &Error{Op: "ListZones", Err: context.Canceled}, false, "context canceled"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.retryable, tc.err.Retryable())
			assert.Equal(t, tc.retryable, IsRetryable(tc.err))
			assert.Equal(t, tc.retryable, IsRetryable(fmt.Errorf("wrapped: %w", tc.err)))
			assert.Contains(t, tc.err.Error(), tc.contains)
			assert.ErrorIs(t, tc.err, tc.err.Err)
		})
	}
}

func TestIsRetryableUnknownErrors(t *testing.T) {
	assert.True(t, IsRetryable(errors.New("mystery")))
	assert.False(t, IsRetryable(context.Canceled))
	assert.False(t, IsRetryable(fmt.Errorf("op: %w", context.Canceled)))
}

func TestWrap(t *testing.T) {
	require.NoError(t, wrap("X", nil, nil))

	err := wrap("X", nil, errors.New("net"))
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, 0, e.StatusCode)
	assert.True(t, e.Retryable())

	err = wrap("X", &http.Response{StatusCode: 403}, errors.New("forbidden"))
	require.ErrorAs(t, err, &e)
	assert.Equal(t, 403, e.StatusCode)
	assert.False(t, e.Retryable())
}
