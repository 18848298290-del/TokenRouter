package anthropic

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/google/uuid"
)

// testSessionString creates a random CLI-style identity using the metadata
// format matching the User-Agent in DefaultHeaders.
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

// TestPayloadWithPrompt builds a streaming CLI-style probe, defaulting a blank prompt to "hi".
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
		"max_tokens":  1024,
		"temperature": 1,
		"stream":      true,
	}, nil
}
