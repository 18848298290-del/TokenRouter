package qoder

import (
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// Decoded fixture values need concrete types before assertions can inspect
// protocol fields. The generic assertion reports unexpected types through the
// test, while request-body reading returns captured bytes so authentication
// fixtures can decode their wire representation.

// qoderFixtureValue asserts that a decoded fixture has type T and returns it.
// Unexpected types fail through require with the actual dynamic type, keeping
// later field assertions tied to the intended representation.
func qoderFixtureValue[T any](t *testing.T, raw any) T {
	t.Helper()
	value, ok := raw.(T)
	require.True(t, ok, "unexpected decoded fixture type: %T", raw)
	return value
}

// readAllString reads the captured request body and returns its bytes as text.
// Read failures stop the fixture with context; the request owner remains
// responsible for body lifetime.
func readAllString(t *testing.T, r *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}
