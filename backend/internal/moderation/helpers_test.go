package moderation

import (
	"database/sql"
	"errors"

	moderationadapter "github.com/TokenFlux/TokenRouter/internal/moderation/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

// ErrUserNotFound marks missing fixture users so the test runtime can exercise
// service-level not-found translation.
var ErrUserNotFound = errors.New("user not found")

// testModerationRuntime supplies production HTTP, image, and Cyber parsing
// adapters for fixtures. Background work launches through an untracked
// goroutine; tests that observe refresh results provide their own gates or
// polling instead of an application task owner.
func testModerationRuntime() Runtime {
	return Runtime{Audit: moderationadapter.NewAuditClient(), SnapshotMedia: moderationadapter.SnapshotMedia, Background: func(_ string, fn func()) {
		// Run the reload separately so the stale-snapshot Check can return while
		// GetMultiple waits on the testcase gate; inline execution would block that
		// release path.
		go fn()
	}, CyberText: openai.IsOpenAICyberWarningText, CyberPolicy: openai.DetectOpenAICyberPolicy, ErrorMessage: upstream.ExtractErrorMessage, MissingRow: func(e error) bool { return errors.Is(e, sql.ErrNoRows) }, MissingUser: func(e error) bool { return errors.Is(e, ErrUserNotFound) }}
}

// buildTextLogForTest normalizes plain text into structured input before
// delegating log construction. It retains full item text for review; the shared
// builder applies summary redaction, excerpt limits, and the supplied retention
// policy.
func (s *ContentModerationService) buildTextLogForTest(input ContentModerationCheckInput, cfg *ContentModerationConfig, action string, flagged bool, highestCategory string, highestScore float64, scores map[string]float64, text string, latency *int, queueDelay *int, errText string) *ContentModerationLog {
	content := ContentModerationInput{Text: text}
	content.Normalize()
	return s.buildStructuredLog(input, cfg, action, flagged, highestCategory, highestScore, scores, content, latency, queueDelay, errText, nil)
}
