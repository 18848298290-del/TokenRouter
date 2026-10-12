package forward

import (
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// Lines reads response lines; the transport adapter controls scanning and size limits.
type Lines interface {
	Scan() bool
	Text() string
	Err() error
}

// Response holds upstream response data and the transport's reader and close function.
type Response struct {
	StatusCode int
	Close      func()
	Runtime    bridge.Runtime
	RequestID  string
	Headers    map[string][]string
	Lines      Lines
	// MaxSSEFrameBytes limits bytes accumulated per frame; zero selects the default limit.
	MaxSSEFrameBytes int
}

// Output writes protocol events synchronously; the adapter manages HTTP status and buffering.
type Output interface {
	CopyHeaders(map[string][]string)
	BeginJSON()
	BeginStream()
	ReverseTools([]byte) []byte
	JSONBytes([]byte)
	ResponsesJSON(*protocolopenai.ResponsesResponse)
	ChatJSON(*protocolopenai.ChatCompletionsResponse)
	Event(string, []byte) (int, error)
	Flush()
	Error(int, string, string)
	Observe(string, string, error, string, string)
}
