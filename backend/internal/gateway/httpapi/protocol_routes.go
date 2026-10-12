package httpapi

import (
	"net/http"
	"slices"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

const (
	ProtocolAnthropicMessages     = protocol.ProtocolAnthropicMessages
	ProtocolOpenAIResponses       = protocol.ProtocolOpenAIResponses
	ProtocolOpenAIChatCompletions = protocol.ProtocolOpenAIChatCompletions
	ProtocolGeminiGenerateContent = protocol.ProtocolGeminiGenerateContent
	ProtocolSystemOne             = protocol.ProtocolSystemOne
	ProtocolEmbeddings            = protocol.ProtocolEmbeddings
	ProtocolImagesGenerations     = protocol.ProtocolImagesGenerations
	ProtocolImagesEdits           = protocol.ProtocolImagesEdits
	ProtocolImageBatches          = protocol.ProtocolImageBatches
	ProtocolVideosGenerations     = protocol.ProtocolVideosGenerations
	ProtocolVideosEdits           = protocol.ProtocolVideosEdits
	ProtocolVideosExtensions      = protocol.ProtocolVideosExtensions
	ProtocolTTS                   = protocol.ProtocolTTS
	ProtocolSTT                   = protocol.ProtocolSTT
	ProtocolCustomVoices          = protocol.ProtocolCustomVoices
	ProtocolVoiceRealtime         = protocol.ProtocolVoiceRealtime
	ProtocolResponsesWebSocket    = protocol.ProtocolResponsesWebSocket
	ProtocolLive                  = protocol.ProtocolLive
	ProtocolResponsesCompact      = protocol.ProtocolResponsesCompact
	ProtocolAlphaSearch           = protocol.ProtocolAlphaSearch
	ProtocolWebSearch             = protocol.ProtocolWebSearch
	ProtocolXSearch               = protocol.ProtocolXSearch
	ProtocolQoderChat             = protocol.ProtocolQoderChat
	ProtocolGeminiBatch           = protocol.ProtocolGeminiBatch
	ProtocolVertexBatch           = protocol.ProtocolVertexBatch
)

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
	// An empty method matches all methods. Prefix matches require a complete path segment.
	Method    string
	Path      string
	Prefix    bool
	WebSocket bool
}

// ProtocolEndpoint holds the shared HTTP display and admission metadata.
type ProtocolEndpoint struct {
	ID       ProtocolID
	Endpoint string
	Routes   []ProtocolRoute
}

// httpProtocol derives the catalogue display from admission route metadata.
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

// ProtocolForRoute resolves a normalized method and path to an extended protocol.
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

// ProtocolEndpoints returns independent copies, including route slices, for callers to modify.
func ProtocolEndpoints() []ProtocolEndpoint {
	out := slices.Clone(protocolEndpoints)
	for i := range out {
		out[i].Routes = slices.Clone(out[i].Routes)
	}
	return out
}
