package protocol

import (
	"slices"
)

// id.go defines persisted protocol identifiers and copies configured fallback
// target lists for independent editing.
// Protocol identifiers are persisted in provider settings and group request
// policies, so their spelling remains independent of display names and URL
// aliases. ResolveRoute uses automatic conversion matching for an absent
// fallback entry and tries a present list in order; nil and empty lists both
// supply no conversion targets. CloneFallbacks retains their stored shapes for
// configuration consumers while copying the map and each target slice.

// These identifiers name client entry protocols and provider execution formats
// shared by capability enumeration, saved configuration, and candidate route selection.
const (
	ProtocolAnthropicMessages     ProtocolID = "anthropic_messages"            // Anthropic Messages requests
	ProtocolOpenAIResponses       ProtocolID = "openai_responses"              // OpenAI Responses requests
	ProtocolOpenAIChatCompletions ProtocolID = "openai_chat_completions"       // OpenAI Chat Completions
	ProtocolGeminiGenerateContent ProtocolID = "gemini_generate_content"       // Gemini content generation
	ProtocolSystemOne             ProtocolID = "systemone"                     // SystemOne decision requests
	ProtocolEmbeddings            ProtocolID = "openai_embeddings"             // OpenAI embedding requests
	ProtocolImagesGenerations     ProtocolID = "openai_images_generations"     // new image generation
	ProtocolImagesEdits           ProtocolID = "openai_images_edits"           // existing image editing
	ProtocolImageBatches          ProtocolID = "image_batches"                 // persisted batch-image jobs
	ProtocolVideosGenerations     ProtocolID = "grok_videos_generations"       // new video generation
	ProtocolVideosEdits           ProtocolID = "grok_videos_edits"             // existing video editing
	ProtocolVideosExtensions      ProtocolID = "grok_videos_extensions"        // existing video extension
	ProtocolTTS                   ProtocolID = "grok_tts"                      // text-to-speech voice generation
	ProtocolSTT                   ProtocolID = "grok_stt"                      // speech-to-text audio transcription
	ProtocolCustomVoices          ProtocolID = "grok_custom_voices"            // custom voice resources
	ProtocolVoiceRealtime         ProtocolID = "grok_voice_realtime"           // realtime voice sessions
	ProtocolResponsesWebSocket    ProtocolID = "openai_responses_websocket"    // Responses WebSocket transport
	ProtocolLive                  ProtocolID = "openai_live"                   // OpenAI Live sessions
	ProtocolResponsesCompact      ProtocolID = "openai_responses_compact"      // Responses history compaction
	ProtocolAlphaSearch           ProtocolID = "openai_alpha_search"           // OpenAI alpha search
	ProtocolWebSearch             ProtocolID = "grok_web_search"               // standalone web search
	ProtocolXSearch               ProtocolID = "grok_x_search"                 // standalone X search
	ProtocolQoderChat             ProtocolID = "qoder_chat"                    // provider-native Qoder chat
	ProtocolGeminiBatch           ProtocolID = "gemini_batch_generate_content" // Gemini batch generation
	ProtocolVertexBatch           ProtocolID = "vertex_batch_prediction"       // Vertex batch prediction
)

// ProtocolID identifies a client or provider protocol; its string value is persisted.
type ProtocolID string

// CloneFallbacks copies the fallback map and each target slice in order.
// A nil map remains nil; present nil or empty target lists retain their shape,
// and callers can modify the copied collections independently of source.
func CloneFallbacks(source map[ProtocolID][]ProtocolID) map[ProtocolID][]ProtocolID {
	if source == nil {
		return nil
	}
	result := make(map[ProtocolID][]ProtocolID, len(source))
	for key, targets := range source {
		result[key] = slices.Clone(targets)
	}
	return result
}
