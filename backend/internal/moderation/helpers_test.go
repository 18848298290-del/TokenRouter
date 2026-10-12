package moderation

import (
	"database/sql"
	"errors"

	moderationadapter "github.com/TokenFlux/TokenRouter/internal/moderation/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/openai"
)

var ErrUserNotFound = errors.New("user not found")

func testModerationRuntime() Runtime {
	return Runtime{Audit: moderationadapter.NewAuditClient(), SnapshotMedia: moderationadapter.SnapshotMedia, Background: func(_ string, fn func()) { go fn() }, CyberText: openai.IsOpenAICyberWarningText, CyberPolicy: openai.DetectOpenAICyberPolicy, ErrorMessage: upstream.ExtractErrorMessage, MissingRow: func(e error) bool { return errors.Is(e, sql.ErrNoRows) }, MissingUser: func(e error) bool { return errors.Is(e, ErrUserNotFound) }}
}

// buildTextLogForTest normalizes text input and builds a moderation log.
func (s *ContentModerationService) buildTextLogForTest(input ContentModerationCheckInput, cfg *ContentModerationConfig, action string, flagged bool, highestCategory string, highestScore float64, scores map[string]float64, text string, latency *int, queueDelay *int, errText string) *ContentModerationLog {
	content := ContentModerationInput{Text: text}
	content.Normalize()
	return s.buildStructuredLog(input, cfg, action, flagged, highestCategory, highestScore, scores, content, latency, queueDelay, errText, nil)
}
