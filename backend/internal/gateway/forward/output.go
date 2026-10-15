package forward

import (
	"github.com/TokenFlux/TokenRouter/internal/protocol/bridge"
	protocolopenai "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// output.go defines upstream response reading and synchronous downstream output
// interfaces for protocol conversion. Converted upstream output needs a
// transport reader and a synchronous downstream writer while protocol
// algorithms remain independent of Gin. Adapters own header filtering, HTTP
// commitment, flushing, and line-size restrictions; conversion supplies
// response values and framed event data.

// Lines supplies the transport adapter's response-line cursor and read-error result.
type Lines interface {
	// Scan advances to the next response line and reports whether one is
	// available. False ends line consumption; callers inspect Err to distinguish
	// normal exhaustion from a transport or scanner failure.
	Scan() bool
	// Text returns the response line selected by the latest successful Scan.
	// Stream conversion reads it before advancing the cursor and performs SSE
	// framing itself, including blank-line event separation.
	Text() string
	// Err returns the error that ended line consumption, or nil for normal
	// exhaustion. Conversion can still capture a complete buffered JSON frame
	// before returning that transport failure to its caller.
	Err() error
}

// Response holds upstream response data and the transport's reader and close function.
type Response struct {
	StatusCode int // upstream HTTP status
	// Close releases the exchange response resource. Successful exchange adapters
	// provide this callback because conversion defers it before consuming the response.
	Close     func()
	Runtime   bridge.Runtime // protocol conversion runtime
	RequestID string         // upstream exchange request identifier
	// Headers contains direct upstream response metadata read by output adapters.
	// Copying Response retains its map and slices, so mutation needs an independent clone.
	Headers map[string][]string
	// Lines supplies streamed upstream data when conversion reads SSE. Its
	// per-line scanner limit remains separate from the accumulated frame-byte limit below.
	Lines Lines
	// MaxSSEFrameBytes bounds accumulated line bytes within one SSE frame.
	// Nonpositive values select the converter's default, independently of any transport line limit.
	MaxSSEFrameBytes int
}

// Output writes protocol events synchronously; the adapter manages HTTP status and buffering.
type Output interface {
	// CopyHeaders submits upstream headers to the adapter's configured response
	// filter before output begins. The adapter chooses allowed headers; conversion
	// callers do not assume every supplied header reaches the client.
	CopyHeaders(map[string][]string)
	// BeginJSON prepares the adapter for JSON response output before converted
	// bytes are written. Header and HTTP-status behavior belongs to the adapter,
	// so this call alone does not prove successful body output.
	BeginJSON()
	// BeginStream prepares SSE response headers and starts the adapter's stream
	// response. The HTTP adapter can commit status at this point, before any
	// converted content frame has been written.
	BeginStream()
	// ReverseTools restores client tool identities in an encoded converted body.
	// The adapter supplies request-specific restoration state; callers consume the
	// returned bytes and leave returned-storage ownership to that implementation.
	ReverseTools([]byte) []byte
	// JSONBytes submits an encoded JSON body through the adapter's response writer.
	// The adapter owns status, commitment, and write-error recording because this
	// synchronous method provides no write-error return to conversion.
	JSONBytes([]byte)
	// ResponsesJSON submits a typed Responses result for synchronous serialization
	// and output. Callers leave its data unchanged during the call while the
	// adapter manages JSON writing and diagnostics.
	ResponsesJSON(*protocolopenai.ResponsesResponse)
	// ChatJSON submits a typed Chat Completions result for synchronous serialization
	// and output. The adapter manages response headers and write diagnostics;
	// callers retain ownership after the method finishes reading the value.
	ChatJSON(*protocolopenai.ChatCompletionsResponse)
	// Event writes an SSE frame and returns the adapter's framed-byte count and
	// write error. Empty event names select unnamed data frames; callers use write
	// outcomes for disconnect handling, then Flush when pending output should advance.
	Event(string, []byte) (int, error)
	// Flush forwards pending stream output through the adapter's response flusher.
	// It has no error return, so conversion relies on Event's write result and
	// transport context for client-disconnect evidence.
	Flush()
	// Error writes a protocol-specific final error using the supplied HTTP status,
	// kind, and message. The adapter owns commitment and the Responses-versus-Chat
	// error shape selected for the current client.
	Error(int, string, string)
	// Observe submits conversion diagnostics with level, message, error, request ID,
	// and event name. The adapter owns logging and observation effects; invoking
	// this method does not itself write a client response.
	Observe(string, string, error, string, string)
}
