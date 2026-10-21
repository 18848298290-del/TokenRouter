// models.types.ts defines media modality names and the model records returned by
// Claude-compatible listings. Marketplace consumers use modality metadata to describe
// accepted and produced media, while provider model selectors use model identifiers and
// display names from listing responses.

// ModelModality identifies media accepted or produced by a catalog model.
export type ModelModality = 'text' | 'image' | 'audio' | 'video' | 'pdf'

// ClaudeModel describes a model returned by Claude-compatible model listing endpoints.
export interface ClaudeModel {
  id: string // Record string identifier.
  type: string // Model object discriminator.
  display_name: string // Human-readable display name.
  created_at: string // Record creation timestamp.
}
