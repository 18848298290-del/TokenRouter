package anthropic

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/google/uuid"
)

// test_payload.go builds streaming Anthropic probe payloads with CLI-style
// system and metadata values.
// Administrative Anthropic probes use the package's CLI-style system prompt and
// metadata format alongside a streaming user message. Random device and session
// identifiers keep each prepared probe self-contained, while a trimmed prompt
// defaults to hi for an empty administrative input.

// testSessionString returns CLI probe metadata with a 64-hex device value and
// new session UUID, formatted for the User-Agent version in DefaultHeaders.
// Device bytes use crypto/rand.Read, whose source errors terminate the process;
// uuid.New panics when session UUID generation fails.
func testSessionString() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	hex64 := hex.EncodeToString(b)
	sessionUUID := uuid.New().String()
	uaVersion := ExtractCLIVersion(DefaultHeaders["User-Agent"])
	return FormatMetadataUserID(hex64, "", sessionUUID, uaVersion), nil
}

// TestPayloadWithPrompt returns a streaming administrative probe for modelID.
// The prompt is trimmed and defaults to hi when blank. The body includes the
// CLI system prompt, cache markers, and fresh session metadata. Identifier
// generation inherits testSessionString's random-read termination and UUID
// panic behavior.
func TestPayloadWithPrompt(modelID string, prompt string) (map[string]any, error) {
	sessionID, err := testSessionString()
	if err != nil {
		return nil, err
	}
	testPrompt := strings.TrimSpace(prompt)
	if testPrompt == "" {
		testPrompt = "hi"
	}

	return map[string]any{
		"model": modelID,
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{
						"type": "text",
						"text": testPrompt,
						"cache_control": map[string]string{
							"type": "ephemeral",
						},
					},
				},
			},
		},
		"system": []map[string]any{
			{
				"type": "text",
				"text": ClaudeCodeSystemPrompt,
				"cache_control": map[string]string{
					"type": "ephemeral",
				},
			},
		},
		"metadata": map[string]string{
			"user_id": sessionID,
		},
		// Bound administrative probe output with a fixed generated-token ceiling.
		"max_tokens":  1024,
		"temperature": 1,
		"stream":      true,
	}, nil
}
