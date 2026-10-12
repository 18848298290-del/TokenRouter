package notification

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/notification/smtp"
	mailtest "github.com/TokenFlux/TokenRouter/internal/notification/testkit"
)

// TestSanitizeEmailHeader_CRLF supplies a subject containing a combined carriage-return and newline sequence.
// It verifies that sanitization removes those line separators while retaining
// the adjacent subject text and its ordinary space.
func TestSanitizeEmailHeader_CRLF(t *testing.T) {
	require.Equal(t, "Subject injected", SanitizeEmailHeader("Subject\r\n injected"))
}

// TestSanitizeEmailHeader_OnlyCR places a lone carriage return between two text fragments.
// It verifies the header sanitizer joins those fragments after removing the
// control character that could otherwise terminate a header line.
func TestSanitizeEmailHeader_OnlyCR(t *testing.T) {
	require.Equal(t, "foobar", SanitizeEmailHeader("foo\rbar"))
}

// TestSanitizeEmailHeader_OnlyLF places a lone newline between two ordinary header fragments.
// It verifies removal of that separator and retention of the text on
// each side in the returned single-line header value.
func TestSanitizeEmailHeader_OnlyLF(t *testing.T) {
	require.Equal(t, "foobar", SanitizeEmailHeader("foo\nbar"))
}

// TestSanitizeEmailHeader_Clean supplies a subject containing no carriage returns or newlines.
// It verifies the sanitizer returns the supplied spelling unchanged so normal
// subject text does not acquire formatting or normalization changes.
func TestSanitizeEmailHeader_Clean(t *testing.T) {
	require.Equal(t, "TokenRouter", SanitizeEmailHeader("TokenRouter"))
}

// TestSanitizeEmailHeader_Empty supplies an empty header value to the sanitizer.
// It verifies the returned string is empty, allowing callers to retain their
// existing handling of an absent subject or sender display name.
func TestSanitizeEmailHeader_Empty(t *testing.T) {
	require.Equal(t, "", SanitizeEmailHeader(""))
}

// TestSanitizeEmailHeader_MultipleNewlines supplies several CRLF-separated fragments in one subject.
// It verifies every separator is removed and all remaining text is retained
// in order in the resulting single-line header value.
func TestSanitizeEmailHeader_MultipleNewlines(t *testing.T) {
	require.Equal(t, "abc", SanitizeEmailHeader("a\r\nb\r\nc"))
}

// TestSMTPContextCancellation cancels delivery before invocation and after a TCP connection is accepted.
// It verifies the returned cancellation errors and zero messages for the
// pre-canceled case, with a bounded wait for the held in-flight send.
func TestSMTPContextCancellation(t *testing.T) {
	t.Run("before-send", func(t *testing.T) {
		repo := mailtest.NewMemorySettings()
		server := mailtest.StartSMTPServer(t)
		require.NoError(t, repo.SetMultiple(context.Background(), server.Settings()))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		require.ErrorIs(t, NewMailer(repo, smtp.New()).SendEmail(ctx, "fixture@example.com", "fixture", "fixture"), context.Canceled)
		require.Zero(t, server.MessageCount())
	})
	t.Run("in-flight", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		// This listener accepts TCP but withholds the SMTP greeting, keeping the
		// send in protocol setup until cancellation; close it when the fixture exits.
		defer func() { _ = ln.Close() }()
		accepted := make(chan net.Conn, 1)
		// Publish the accepted connection to the test so cancellation begins only
		// after the client's dial has reached the fixture's intentionally stalled SMTP setup.
		go func() {
			conn, e := ln.Accept()
			if e == nil {
				accepted <- conn
			}
		}()
		repo := mailtest.NewMemorySettings()
		address, ok := ln.Addr().(*net.TCPAddr)
		require.True(t, ok)
		require.NoError(t, repo.SetMultiple(context.Background(), map[string]string{SettingKeySMTPHost: "127.0.0.1", SettingKeySMTPPort: fmt.Sprint(address.Port), SettingKeySMTPFrom: "sender@example.com", SettingKeySMTPUsername: "fixture", SettingKeySMTPPassword: "fixture"}))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		// Run the send independently while the fixture holds protocol setup.
		// The completion channel exposes cancellation to the test without releasing a greeting first.
		go func() {
			done <- NewMailer(repo, smtp.New()).SendEmail(ctx, "fixture@example.com", "fixture", "fixture")
		}()
		conn := <-accepted
		// The test owns the accepted socket after publication. Release it on exit
		// even if the client's cancellation result fails the bounded completion assertion below.
		defer func() { _ = conn.Close() }()
		cancel()
		// Cancellation must interrupt the established protocol wait. Bound observation
		// so a transport that ignores the context reports a failure instead of holding this test indefinitely.
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(time.Second):
			t.Fatal("SMTP 未响应取消")
		}
	})
}
