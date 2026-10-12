package gemini

import (
	mathrand "math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	googlewire "github.com/TokenFlux/TokenRouter/internal/protocol/google"
)

const (
	geminiRetryBaseDelay = time.Second
	geminiRetryMaxDelay  = 16 * time.Second
)

func IsGeminiSignatureRelatedError(respBody []byte) bool {
	msg := strings.ToLower(strings.TrimSpace(googlewire.ExtractPlatformMessage(respBody)))
	if msg == "" {
		msg = strings.ToLower(string(respBody))
	}
	return strings.Contains(msg, "thought_signature") || strings.Contains(msg, "signature")
}

func SleepGeminiBackoff(attempt int) {
	delay := min(geminiRetryBaseDelay*time.Duration(1<<uint(attempt-1)), geminiRetryMaxDelay)

	// +/- 20% jitter
	r := mathrand.New(mathrand.NewSource(time.Now().UnixNano()))
	jitter := time.Duration(float64(delay) * 0.2 * (r.Float64()*2 - 1))
	sleepFor := max(delay+jitter, 0)
	time.Sleep(sleepFor)
}

func IsGeminiInsufficientScope(headers http.Header, body []byte) bool {
	if strings.Contains(strings.ToLower(headers.Get("Www-Authenticate")), "insufficient_scope") {
		return true
	}
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "insufficient authentication scopes") || strings.Contains(lower, "access_token_scope_insufficient")
}

func EstimateGeminiCountTokens(reqBody []byte) int {
	total := 0

	// systemInstruction.parts[].text
	gjson.GetBytes(reqBody, "systemInstruction.parts").ForEach(func(_, part gjson.Result) bool {
		if t := strings.TrimSpace(part.Get("text").String()); t != "" {
			total += EstimateTokensForText(t)
		}
		return true
	})

	// contents[].parts[].text
	gjson.GetBytes(reqBody, "contents").ForEach(func(_, content gjson.Result) bool {
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			if t := strings.TrimSpace(part.Get("text").String()); t != "" {
				total += EstimateTokensForText(t)
			}
			return true
		})
		return true
	})

	if total < 0 {
		return 0
	}
	return total
}

func EstimateTokensForText(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	runes := []rune(s)
	if len(runes) == 0 {
		return 0
	}
	ascii := 0
	for _, r := range runes {
		if r <= 0x7f {
			ascii++
		}
	}
	asciiRatio := float64(ascii) / float64(len(runes))
	if asciiRatio >= 0.8 {
		// Roughly 4 chars per token for English-like text.
		return (len(runes) + 3) / 4
	}
	// Use one rune per token when more than 20% of the text is non-ASCII.
	return len(runes)
}
