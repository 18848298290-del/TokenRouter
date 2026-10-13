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

// Claude Code probes can be answered locally before provider forwarding begins.
// Synthetic Messages responses retain message IDs, stop fields and usage shape
// expected by the client; streaming emits the same staged block lifecycle while
// unary one-token Haiku probes carry their max_tokens stop reason.

// WriteInterceptStream emits an Anthropic-shaped synthetic stream for local
// probe interception. Suggestion mode returns empty text with one output token;
// other modes use the warmup text and two tokens. Each event is flushed with a
// short pause, and write errors are discarded because this local response has
// no upstream attempt to retry.
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

	// Emit each synthetic lifecycle event with a separate flush and pause; failed
	// client writes do not trigger an upstream retry for this intercepted local
	// answer.
	for _, event := range events {
		_, _ = c.Writer.WriteString(event + "\n\n")
		c.Writer.Flush()
		time.Sleep(20 * time.Millisecond)
	}
}

// GenerateInterceptMessageID generates msg_01 plus twenty-two Base62 characters
// from random bytes, with modulo mapping rather than a collision check. The
// pinned Go runtime fills the random slice or terminates on entropy failure;
// its Read implementation never returns the error that selects the defensive
// timestamp branch below.
func GenerateInterceptMessageID() string {
	const charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	// Match the twenty-two-character suffix accepted by the Messages probe shape.
	const idLen = 22
	randomBytes := make([]byte, idLen)
	if _, err := rand.Read(randomBytes); err != nil {
		// This branch defines a timestamp-shaped ID if Read returns an error. The
		// pinned Go implementation terminates on entropy failure before this path
		// can provide a response.
		return fmt.Sprintf("msg_01%022d", time.Now().UnixNano())
	}
	b := make([]byte, idLen)
	for i := range b {
		b[i] = charset[int(randomBytes[i])%len(charset)]
	}
	return "msg_01" + string(b)
}

// WriteInterceptResponse writes an Anthropic-shaped synthetic JSON result for
// intercepted probes. Suggestion mode returns empty text, the one-token Haiku
// probe returns # with max_tokens termination, and other modes return warmup
// text; synthetic usage and model metadata are provided directly.
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
		// The one-token probe needs its requested limit reflected in termination,
		// so its fixed one-character answer uses max_tokens rather than end_turn.
		stopReason = "max_tokens"
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
