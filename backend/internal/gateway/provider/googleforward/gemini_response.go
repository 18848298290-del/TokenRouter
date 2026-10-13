package googleforward

import (
	"io"
	"net/http"

	gemininative "github.com/TokenFlux/TokenRouter/internal/upstream/gemini"
)

// Gemini response callbacks connect native readers and compatibility converters
// to the same request output. Bounded body reading and header filtering remain
// HTTP operations, while the attempt receives image observations and restores
// tool names during synchronous response adaptation.

// geminiResponseAdapter binds provider response reading to this attempt image
// and tool state. The executor owns response-body closure; callbacks borrow
// its reader for bounded reads and apply the injected HTTP header policy.
func (s *Gemini) geminiResponseAdapter(c *attempt) *gemininative.ResponseAdapter {
	return &gemininative.ResponseAdapter{Options: gemininative.ResponseOptions{
		GoogleError: func(status int, message string) error { return c.GoogleError(status, message) },

		ReadBody: func(body io.Reader) ([]byte, error) {
			return c.ReadBody(body, s.Options.ResponseReadLimit)
		},
		WriteHeaders:    func(dst, src http.Header) { c.WriteHeaders(dst, src, s.HeaderFilter) },
		DebugHeaders:    s.Options.Configured && s.Options.DebugHeaders,
		HasHeaderFilter: s.HeaderFilter != nil,

		ObserveImages: func(body []byte) { c.observeImages(body) },
		ReverseTools:  func(body []byte) []byte { return c.reverseTools(body) },
		ClaudeError:   func(status int, kind, message string) error { return c.ClaudeError(status, kind, message) },
		ChatError: func(status int, kind, message string) error {
			return c.ChatError(status, kind, message)
		},
		CompatError: func(protocol gemininative.OpenAICompatProtocol, status int, kind, message string) error {
			return c.GeminiOpenAICompatError(protocol, status, kind, message)
		},
	}}
}
