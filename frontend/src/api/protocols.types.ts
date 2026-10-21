// protocols.types.ts names the protocol identifiers shared by provider capabilities and
// group admission. The same identifiers appear in route availability, model metadata, and
// request configuration, allowing consumers to compare client-facing protocols with the
// provider's supported upstream interfaces.

// ProtocolID identifies native provider capabilities and client protocols admitted by
// groups.
export type ProtocolID =
  | 'anthropic_messages'
  | 'openai_responses'
  | 'openai_chat_completions'
  | 'gemini_generate_content'
  | 'systemone'
  | 'openai_embeddings'
  | 'openai_images_generations'
  | 'openai_images_edits'
  | 'image_batches'
  | 'grok_videos_generations'
  | 'grok_videos_edits'
  | 'grok_videos_extensions'
  | 'grok_tts'
  | 'grok_stt'
  | 'grok_custom_voices'
  | 'grok_voice_realtime'
  | 'openai_responses_websocket'
  | 'openai_live'
  | 'openai_responses_compact'
  | 'openai_alpha_search'
  | 'grok_web_search'
  | 'grok_x_search'
  | 'qoder_chat'
  | 'gemini_batch_generate_content'
  | 'vertex_batch_prediction'
