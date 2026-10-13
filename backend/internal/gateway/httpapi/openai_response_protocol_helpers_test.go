package httpapi

import (
	"strings"
	"testing"
)

// TestSummarizeNoOutputBody_RespectsLogBodyConfig summarizes an in-progress
// image stream with body logging disabled. It checks that the last event
// remains visible while the raw body excerpt is omitted.
func TestSummarizeNoOutputBody_RespectsLogBodyConfig(t *testing.T) {
	body := []byte("data: {\"type\":\"response.in_progress\",\"response\":{\"status\":\"in_progress\"}}\n\n")
	svc := newImagesFixture(imagesFixtureInputs{})

	summary := svc.Output.ImageNoOutputSummary(body)
	if !strings.Contains(summary, "last_event=response.in_progress") {
		t.Fatalf("summary should keep structured diagnostics, got %q", summary)
	}
	if strings.Contains(summary, " body=") {
		t.Fatalf("summary should omit body when log_upstream_error_body is disabled, got %q", summary)
	}
}
