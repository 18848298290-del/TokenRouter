package openaiforward

import (
	"bufio"
	"io"
	"strings"
	"testing"
	"time"
)

// TestAnthropicNativeLinePump_TimesOutWithoutData leaves a pipe reader open
// with no producer data. Next must return the idle-timeout error within the
// test's broad elapsed-time bound.
func TestAnthropicNativeLinePump_TimesOutWithoutData(t *testing.T) {
	pr, _ := io.Pipe()
	scanner := bufio.NewScanner(pr)
	defer func() { _ = pr.Close() }()

	pump := NewAnthropicLinePump(scanner, 50*time.Millisecond)
	defer pump.Stop()

	start := time.Now()
	_, err := pump.Next()
	if err == nil || !strings.Contains(err.Error(), "stream data interval timeout") {
		t.Fatalf("expected interval timeout, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout not respected: %v", elapsed)
	}
}

// TestAnthropicNativeLinePump_DataResetsTimer supplies one line then pauses the
// producer while keeping the pipe open. The first Next returns that line and
// the next wait must report idle timeout.
func TestAnthropicNativeLinePump_DataResetsTimer(t *testing.T) {
	pr, pw := io.Pipe()
	scanner := bufio.NewScanner(pr)
	pump := NewAnthropicLinePump(scanner, 1*time.Second)
	defer pump.Stop()

	// Send one line before withholding further data; keeping the writer open
	// distinguishes interval expiry from ordinary scanner EOF during the next
	// wait.
	go func() {
		_, _ = pw.Write([]byte("event: ping\n"))
		// Delay beyond the configured interval so the second consumer wait expires
		// before the producer closes the pipe and publishes EOF.
		time.Sleep(3 * time.Second)
		_ = pw.Close()
	}()
	defer func() { _ = pr.Close() }()

	line, err := pump.Next()
	if err != nil || line != "event: ping" {
		t.Fatalf("expected first line, got %q err=%v", line, err)
	}

	start := time.Now()
	_, err = pump.Next()
	if err == nil || !strings.Contains(err.Error(), "stream data interval timeout") {
		t.Fatalf("expected interval timeout after data stops, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout not respected: %v", elapsed)
	}
}
