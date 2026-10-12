package qoder

import (
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// qoderFixtureValue returns the decoded fixture as T or fails the test.
func qoderFixtureValue[T any](t *testing.T, raw any) T {
	t.Helper()
	value, ok := raw.(T)
	require.True(t, ok, "unexpected decoded fixture type: %T", raw)
	return value
}

func readAllString(t *testing.T, r *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}
