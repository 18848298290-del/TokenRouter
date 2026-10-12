package mediaentry

import (
	"testing"
	"time"
)

// Realtime audio billing needs actual observed audio and positive connection
// duration. These fixtures inspect refusal of unobserved/empty sessions and
// the generated identity and minutes-based quantity for two independent
// measured audio connections.

// TestGrokRealtimeBillingResultRequiresObservedAudio supplies an unobserved
// session and a zero-duration observed session. It checks both return no
// billing result before any completion recorder or financial operation is
// involved.
func TestGrokRealtimeBillingResultRequiresObservedAudio(t *testing.T) {
	if grokRealtimeBillingResult("grok-voice-latest", time.Second, false) != nil {
		t.Fatal("a session without observed audio must not be billed")
	}
	if grokRealtimeBillingResult("grok-voice-latest", 0, true) != nil {
		t.Fatal("zero-duration sessions must not be billed")
	}
}

// TestGrokRealtimeBillingResultUsesForcedUniqueID creates two independent
// ninety-second observed sessions. It checks a nonempty first request ID,
// different IDs for the two results and one and a half Realtime minutes in
// the first returned usage record.
func TestGrokRealtimeBillingResultUsesForcedUniqueID(t *testing.T) {
	first := grokRealtimeBillingResult("grok-voice-latest", 90*time.Second, true)
	second := grokRealtimeBillingResult("grok-voice-latest", 90*time.Second, true)
	if first == nil || second == nil {
		t.Fatal("observed audio sessions should be billable")
	}
	if first.RequestID == "" {
		t.Fatalf("unexpected billing request ID %q", first.RequestID)
	}
	if first.RequestID == second.RequestID {
		t.Fatal("independent realtime connections must not share a billing request ID")
	}
	if first.AudioUsage == nil || first.AudioUsage.Mode != "realtime" || first.AudioUsage.DurationOrUnits != 1.5 {
		t.Fatalf("unexpected audio usage: %#v", first.AudioUsage)
	}
}
