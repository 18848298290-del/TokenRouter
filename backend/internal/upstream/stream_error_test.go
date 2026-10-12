package upstream

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSanitizeStreamError_StripsNetworkAddresses builds a read failure
// containing both source and destination addresses. It checks that the client
// label retains the reset cause and omits those addresses and ports.
func TestSanitizeStreamError_StripsNetworkAddresses(t *testing.T) {
	src, err := net.ResolveTCPAddr("tcp", "10.0.0.1:54321")
	require.NoError(t, err)
	dst, err := net.ResolveTCPAddr("tcp", "52.1.2.3:443")
	require.NoError(t, err)

	raw := &net.OpError{
		Op:     "read",
		Net:    "tcp",
		Source: src,
		Addr:   dst,
		Err:    syscall.ECONNRESET,
	}

	// Check the raw error first so the address-removal assertions exercise
	// an error that actually contains both endpoints and their diagnostic details.
	require.Contains(t, raw.Error(), "10.0.0.1")
	require.Contains(t, raw.Error(), "52.1.2.3")

	got := SanitizeStreamError(raw)
	require.NotContains(t, got, "10.0.0.1", "不得泄露内部源 IP")
	require.NotContains(t, got, "54321", "不得泄露源端口")
	require.NotContains(t, got, "52.1.2.3", "不得泄露上游目标 IP")
	require.NotContains(t, got, "443", "不得泄露上游端口")
	require.Equal(t, "connection reset by peer", got)
}

// TestSanitizeStreamError_KnownErrors supplies standard stream, context, and
// syscall errors plus nil and an unrecognized message. It checks the exact
// client label chosen for each case.
func TestSanitizeStreamError_KnownErrors(t *testing.T) {
	cases := []struct {
		name string // named stream failure scenario
		err  error  // input stream failure value
		want string // expected client error label
	}{
		{"unexpected EOF", io.ErrUnexpectedEOF, "unexpected EOF"},
		{"EOF", io.EOF, "EOF"},
		{"context canceled", context.Canceled, "canceled"},
		{"deadline exceeded", context.DeadlineExceeded, "deadline exceeded"},
		{"ECONNRESET 直接", syscall.ECONNRESET, "connection reset by peer"},
		{"EPIPE", syscall.EPIPE, "broken pipe"},
		{"ETIMEDOUT", syscall.ETIMEDOUT, "connection timed out"},
		{"未识别错误兜底", errors.New("weird internal error"), "upstream connection error"},
		{"nil 返回空串", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, SanitizeStreamError(tc.err))
		})
	}
}
