package provider

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTempUnschedState(t *testing.T) {
	now := time.Now()
	until := now.Add(5 * time.Minute)

	state := &TempUnschedState{
		UntilUnix:       until.Unix(),
		TriggeredAtUnix: now.Unix(),
		StatusCode:      503,
		MatchedKeyword:  "overloaded",
		RuleIndex:       0,
		ErrorMessage:    "Server is overloaded",
	}

	require.Equal(t, 503, state.StatusCode)
	require.Equal(t, "overloaded", state.MatchedKeyword)
	require.Equal(t, 0, state.RuleIndex)

	require.Equal(t, until.Unix(), state.UntilUnix)
	require.Equal(t, now.Unix(), state.TriggeredAtUnix)
}
