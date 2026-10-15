package httpapi

import (
	"net/http"
	"slices"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// protocol_routes.go maps extended gateway routes to protocol admission and
// display metadata.
// Extended gateway routes share admission metadata with the protocol display
// catalogue. Matching checks methods and complete prefix segments, while core
// text entries and upstream-only entries can carry display information without
// extended routes; those entries are gated by their dedicated handlers.

// These aliases retain shared protocol identifiers for HTTP display and group
// admission metadata; they do not introduce separate persisted capability IDs.
const (
	ProtocolAnthropicMessages     = protocol.ProtocolAnthropicMessages     // Anthropic Messages client protocol
	ProtocolOpenAIResponses       = protocol.ProtocolOpenAIResponses       // OpenAI Responses client protocol
	ProtocolOpenAIChatCompletions = protocol.ProtocolOpenAIChatCompletions // OpenAI Chat client protocol
	ProtocolGeminiGenerateContent = protocol.ProtocolGeminiGenerateContent // Gemini native text protocol
	ProtocolSystemOne             = protocol.ProtocolSystemOne             // SystemOne decision request protocol
	ProtocolEmbeddings            = protocol.ProtocolEmbeddings            // embedding request protocol
	ProtocolImagesGenerations     = protocol.ProtocolImagesGenerations     // image generation request protocol
	ProtocolImagesEdits           = protocol.ProtocolImagesEdits           // image editing request protocol
	ProtocolImageBatches          = protocol.ProtocolImageBatches          // persistent image job protocol
	ProtocolVideosGenerations     = protocol.ProtocolVideosGenerations     // video generation request protocol
	ProtocolVideosEdits           = protocol.ProtocolVideosEdits           // video editing request protocol
	ProtocolVideosExtensions      = protocol.ProtocolVideosExtensions      // video extension request protocol
	ProtocolTTS                   = protocol.ProtocolTTS                   // speech synthesis request protocol
	ProtocolSTT                   = protocol.ProtocolSTT                   // speech transcription request protocol
	ProtocolCustomVoices          = protocol.ProtocolCustomVoices          // custom voice resource protocol
	ProtocolVoiceRealtime         = protocol.ProtocolVoiceRealtime         // realtime voice WebSocket protocol
	ProtocolResponsesWebSocket    = protocol.ProtocolResponsesWebSocket    // Responses WebSocket client protocol
	ProtocolLive                  = protocol.ProtocolLive                  // Live session creation protocol
	ProtocolResponsesCompact      = protocol.ProtocolResponsesCompact      // Responses compact request protocol
	ProtocolAlphaSearch           = protocol.ProtocolAlphaSearch           // Alpha search request protocol
	ProtocolWebSearch             = protocol.ProtocolWebSearch             // Web search request protocol
	ProtocolXSearch               = protocol.ProtocolXSearch               // X search request protocol
	ProtocolQoderChat             = protocol.ProtocolQoderChat             // Qoder upstream chat protocol
	ProtocolGeminiBatch           = protocol.ProtocolGeminiBatch           // Gemini upstream batch protocol
	ProtocolVertexBatch           = protocol.ProtocolVertexBatch           // Vertex upstream batch protocol
)

// protocolEndpoints combines display-only entries with normalized extended
// route metadata; ProtocolEndpoints clones it for catalogue consumers.
var protocolEndpoints = []ProtocolEndpoint{
	{ID: ProtocolAnthropicMessages, Endpoint: "POST /v1/messages"},
	{ID: ProtocolOpenAIResponses, Endpoint: "POST /v1/responses"},
	{ID: ProtocolOpenAIChatCompletions, Endpoint: "POST /v1/chat/completions"},
	{ID: ProtocolGeminiGenerateContent, Endpoint: "POST /v1beta/models/{model}:generateContent / :streamGenerateContent"},
	httpProtocol(ProtocolSystemOne, ProtocolRoute{Method: http.MethodPost, Path: "/systemone"}),
	httpProtocol(ProtocolEmbeddings, ProtocolRoute{Method: http.MethodPost, Path: "/embeddings"}),
	httpProtocol(ProtocolImagesGenerations, ProtocolRoute{Method: http.MethodPost, Path: "/images/generations"}),
	httpProtocol(ProtocolImagesEdits, ProtocolRoute{Method: http.MethodPost, Path: "/images/edits"}),
	httpProtocol(ProtocolImageBatches, ProtocolRoute{Method: http.MethodPost, Path: "/images/batches"}),
	httpProtocol(ProtocolVideosGenerations, ProtocolRoute{Method: http.MethodPost, Path: "/videos/generations"}, ProtocolRoute{Method: http.MethodPost, Path: "/videos"}),
	httpProtocol(ProtocolVideosEdits, ProtocolRoute{Method: http.MethodPost, Path: "/videos/edits"}),
	httpProtocol(ProtocolVideosExtensions, ProtocolRoute{Method: http.MethodPost, Path: "/videos/extensions"}),
	httpProtocol(ProtocolTTS, ProtocolRoute{Method: http.MethodPost, Path: "/tts"}),
	httpProtocol(ProtocolSTT, ProtocolRoute{Method: http.MethodPost, Path: "/stt"}),
	httpProtocol(ProtocolCustomVoices, ProtocolRoute{Path: "/custom-voices", Prefix: true}),
	httpProtocol(ProtocolVoiceRealtime, ProtocolRoute{Method: http.MethodGet, Path: "/realtime", WebSocket: true}),
	httpProtocol(ProtocolResponsesWebSocket, ProtocolRoute{Method: http.MethodGet, Path: "/responses", WebSocket: true}),
	httpProtocol(ProtocolLive, ProtocolRoute{Method: http.MethodPost, Path: "/live"}, ProtocolRoute{Method: http.MethodPost, Path: "/realtime/calls"}),
	httpProtocol(ProtocolResponsesCompact, ProtocolRoute{Method: http.MethodPost, Path: "/responses/compact"}),
	httpProtocol(ProtocolAlphaSearch, ProtocolRoute{Method: http.MethodPost, Path: "/alpha/search"}),
	httpProtocol(ProtocolWebSearch, ProtocolRoute{Method: http.MethodPost, Path: "/web_search"}),
	httpProtocol(ProtocolXSearch, ProtocolRoute{Method: http.MethodPost, Path: "/x_search"}),
	{ID: ProtocolQoderChat, Endpoint: "agent_chat_generation (SSE)"},
	{ID: ProtocolGeminiBatch, Endpoint: "POST /v1beta/models/{model}:batchGenerateContent"},
	{ID: ProtocolVertexBatch, Endpoint: "POST /v1/projects/{project}/locations/{location}/batchPredictionJobs"},
}

// ProtocolID identifies a protocol in the shared catalogue.
type ProtocolID = protocol.ProtocolID

// ProtocolRoute describes a normalized endpoint subject to group admission; routing removes alias prefixes.
type ProtocolRoute struct {
	// Method constrains route matching when nonempty; an empty method admits
	// every HTTP method for this route before resource ownership checks.
	Method string
	Path   string // normalized endpoint path
	Prefix bool   // slash-segment child matching enabled
	// WebSocket adds the display annotation in httpProtocol. ProtocolForRoute
	// matches method and path, leaving upgrade validation to the endpoint handler.
	WebSocket bool
}

// ProtocolEndpoint holds the shared HTTP display and admission metadata.
type ProtocolEndpoint struct {
	ID       ProtocolID // shared protocol identifier
	Endpoint string     // displayed method and endpoint
	// Routes supplies normalized extended admission paths. Nil routes keep this
	// entry available for display while dedicated text or upstream handling
	// supplies its separate behavior.
	Routes []ProtocolRoute
}

// httpProtocol builds display text and route metadata from one primary route
// plus aliases. The display uses the /v1 prefix and optional method and
// WebSocket annotation; matching retains normalized route paths for admission.
func httpProtocol(id ProtocolID, primary ProtocolRoute, aliases ...ProtocolRoute) ProtocolEndpoint {
	endpoint := "/v1" + primary.Path
	if primary.Method != "" {
		endpoint = primary.Method + " " + endpoint
	}
	if primary.WebSocket {
		endpoint += " (WebSocket)"
	}
	return ProtocolEndpoint{ID: id, Endpoint: endpoint, Routes: append([]ProtocolRoute{primary}, aliases...)}
}

// ProtocolForRoute matches supplied method and normalized path against extended
// route metadata. Empty route methods accept every method, and prefixes require
// a slash-separated child path. Entries with no Routes do not match; callers
// normalize aliases and apply separate text protocol gates.
func ProtocolForRoute(method, path string) (ProtocolID, bool) {
	for _, protocol := range protocolEndpoints {
		for _, route := range protocol.Routes {
			if route.Method != "" && route.Method != method {
				continue
			}
			if path == route.Path || (route.Prefix && strings.HasPrefix(path, route.Path+"/")) {
				return protocol.ID, true
			}
		}
	}
	return "", false
}

// ProtocolEndpoints returns a copied catalogue with independently cloned route
// slices. Callers may edit the returned entries and routes; source admission
// metadata remains unchanged, while immutable strings and scalar route fields
// copy by value.
func ProtocolEndpoints() []ProtocolEndpoint {
	out := slices.Clone(protocolEndpoints)
	for i := range out {
		out[i].Routes = slices.Clone(out[i].Routes)
	}
	return out
}
