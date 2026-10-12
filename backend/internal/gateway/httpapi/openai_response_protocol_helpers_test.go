package httpapi

import (
	"strings"
	"testing"
)

// TestSummarizeNoOutputBody_RespectsLogBodyConfig checks structured diagnostics with error-body logging
// disabled.
// gateway.log_upstream_error_body controls raw upstream body excerpts.
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
