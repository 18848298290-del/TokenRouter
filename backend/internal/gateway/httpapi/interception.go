package httpapi

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/gateway/clientmeta"
)

// WriteInterceptStream writes a synthetic streaming response for an intercepted request.
func WriteInterceptStream(c *gin.Context, model string, interceptType clientmeta.InterceptType) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	var msgID string
	var outputTokens int
	var textDeltas []string

	switch interceptType {
	case clientmeta.InterceptTypeSuggestionMode:
		msgID = GenerateInterceptMessageID()
		outputTokens = 1
		textDeltas = []string{""} // Suggestion probes return empty text.
	default: // clientmeta.InterceptTypeWarmup
		msgID = GenerateInterceptMessageID()
		outputTokens = 2
		textDeltas = []string{"New", " Conversation"}
	}

	// Emit message_start with Anthropic message and usage fields.
	messageStartJSON := `{"type":"message_start","message":{"model":` + strconv.Quote(model) + `,"id":` + strconv.Quote(msgID) + `,"type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"stop_details":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`

	events := []string{
		`event: message_start` + "\n" + `data: ` + messageStartJSON,
		`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	}

	for _, text := range textDeltas {
		deltaJSON := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + strconv.Quote(text) + `}}`
		events = append(events, `event: content_block_delta`+"\n"+`data: `+deltaJSON)
	}

	// The terminal message_delta carries the synthetic response's output token count.
	messageDeltaJSON := `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null,"stop_details":null},"usage":{"output_tokens":` + strconv.Itoa(outputTokens) + `}}`

	events = append(events,
		`event: content_block_stop`+"\n"+`data: {"index":0,"type":"content_block_stop"}`,
		`event: message_delta`+"\n"+`data: `+messageDeltaJSON,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`,
	)

	for _, event := range events {
		_, _ = c.Writer.WriteString(event + "\n\n")
		c.Writer.Flush()
		time.Sleep(20 * time.Millisecond)
	}
}

// GenerateInterceptMessageID returns msg_01 followed by 22 Base62 characters.
func GenerateInterceptMessageID() string {
	const charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	const idLen = 22
	randomBytes := make([]byte, idLen)
	if _, err := rand.Read(randomBytes); err != nil {
		// If entropy is unavailable, retain the expected ID shape using a timestamp.
		return fmt.Sprintf("msg_01%022d", time.Now().UnixNano())
	}
	b := make([]byte, idLen)
	for i := range b {
		b[i] = charset[int(randomBytes[i])%len(charset)]
	}
	return "msg_01" + string(b)
}

// WriteInterceptResponse writes a synthetic non-streaming response for an intercepted request.
func WriteInterceptResponse(c *gin.Context, model string, interceptType clientmeta.InterceptType) {
	var msgID, text, stopReason string
	var outputTokens int

	switch interceptType {
	case clientmeta.InterceptTypeSuggestionMode:
		msgID = GenerateInterceptMessageID()
		text = ""
		outputTokens = 1
		stopReason = "end_turn"
	case clientmeta.InterceptTypeMaxTokensOneHaiku:
		msgID = GenerateInterceptMessageID()
		text = "#"
		outputTokens = 1
		stopReason = "max_tokens" // A max_tokens=1 probe reports max_tokens as its stop reason.
	default: // clientmeta.InterceptTypeWarmup
		msgID = GenerateInterceptMessageID()
		text = "New Conversation"
		outputTokens = 2
		stopReason = "end_turn"
	}

	// Use the Anthropic message shape for the synthetic response.
	response := gin.H{
		"model":         model,
		"id":            msgID,
		"type":          "message",
		"role":          "assistant",
		"content":       []gin.H{{"type": "text", "text": text}},
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"stop_details":  nil,
		"usage": gin.H{
			"input_tokens":                10,
			"cache_creation_input_tokens": 0,
			"cache_read_input_tokens":     0,
			"cache_creation": gin.H{
				"ephemeral_5m_input_tokens": 0,
				"ephemeral_1h_input_tokens": 0,
			},
			"output_tokens": outputTokens,
		},
	}

	c.JSON(http.StatusOK, response)
}
