package egress

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTLSFingerprintCollectorServiceSessionLimits inserts two captures into a
// one-record session using a controlled clock. It checks newest-arrival
// retention, readability at the exact expiration timestamp, and rejection one
// nanosecond later.
func TestTLSFingerprintCollectorServiceSessionLimits(t *testing.T) {
	now := time.Now()
	sessions := NewCaptureSessions(func() time.Time { return now })
	session, err := sessions.Create("https://collector.local", "", time.Minute)
	require.NoError(t, err)
	require.NoError(t, sessions.Append(session.Token, &TLSFingerprintCaptureRecord{ID: "1", CapturedAt: now}, 1))
	require.NoError(t, sessions.Append(session.Token, &TLSFingerprintCaptureRecord{ID: "2", CapturedAt: now.Add(time.Second)}, 1))
	records, err := sessions.List(session.Token)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "2", records[0].ID)
	now = now.Add(time.Minute)
	_, err = sessions.List(session.Token)
	require.NoError(t, err, "保持恰好到期时仍可读取的原边界")
	now = now.Add(time.Nanosecond)
	_, err = sessions.List(session.Token)
	require.Error(t, err)
}
