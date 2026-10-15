package provider

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
)

// test_streams_adaptive.go parses adaptive Anthropic test streams and forwards
// their content and errors.
// Adaptive Messages parsing emits content and errors while leaving whole-test
// completion to the CN coordinator. message_stop and DONE finish the endpoint
// successfully, EOF before those markers fails, and malformed frame JSON is
// skipped while later events remain available for parsing.

// AdaptiveAnthropic emits text from newline-terminated native Messages SSE and
// returns nil on message_stop or DONE. It emits no success-completion event, so
// the coordinator can check later endpoints; premature EOF and upstream errors
// fail, and callers close the body. An EOF fragment is discarded before event
// parsing.
func (s TestStreamOutput) AdaptiveAnthropic(c *TestRun, body io.Reader) error {
	reader := bufio.NewReader(body)
	for {
		line, err := reader.ReadString('\n')
		// Discard a returned EOF fragment before inspecting its event. A terminal
		// marker therefore needs a newline to establish endpoint success in this
		// adaptive parser.
		if err != nil {
			if err == io.EOF {
				return s.Error(c, "Adaptive Anthropic stream ended before message_stop")
			}
			return s.Error(c, fmt.Sprintf("Adaptive Anthropic stream read error: %s", err.Error()))
		}

		line = strings.TrimSpace(line)
		if line == "" || !sseDataPrefix.MatchString(line) {
			continue
		}
		jsonStr := sseDataPrefix.ReplaceAllString(line, "")
		if jsonStr == "[DONE]" {
			return nil
		}

		var data map[string]any
		// Skip unusable frame JSON while retaining the requirement for a terminal
		// endpoint marker, so malformed data alone cannot complete this adaptive
		// check.
		if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
			continue
		}
		switch eventType, _ := data["type"].(string); eventType {
		case "content_block_delta":
			if delta, ok := data["delta"].(map[string]any); ok {
				if text, ok := delta["text"].(string); ok && text != "" {
					s.SendEvent(c, providercore.TestEvent{Type: "content", Text: text})
				}
			}
		case "message_stop":
			return nil
		case "error":
			// An error event can omit usable message details. Keep the endpoint's
			// generic failure label until the expected error object supplies nonempty
			// text.
			errorMsg := "Unknown error"
			if errData, ok := data["error"].(map[string]any); ok {
				if message, ok := errData["message"].(string); ok && message != "" {
					errorMsg = message
				}
			}
			return s.Error(c, fmt.Sprintf("Adaptive Anthropic endpoint error: %s", errorMsg))
		}
	}
}
