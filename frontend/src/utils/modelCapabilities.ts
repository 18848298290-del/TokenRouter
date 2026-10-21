import type { MarketplaceModelPricing } from '@/api/marketplace.types'
import type { ModelModality } from '@/api/models.types'

/**
 * modelCapabilities.ts resolves input and output modality badges for model displays.
 * Supplied capability metadata is accepted when both sides retain recognized modalities
 * after filtering. Otherwise the resolver starts with text input and output, then
 * applies local model-name patterns and selected pricing fields to infer presentation
 * values.
 *
 * Those heuristics interact: image and video generation remove text output, audio
 * patterns alter their respective sides, and gpt-image editing adds image input.
 * Ordinary vision inference is suppressed for generative and audio models so a shared
 * name fragment such as gpt-4o does not automatically add vision. The resulting
 * modalities are deduplicated and ordered for consistent badge layout.
 */

export type { ModelModality } from '@/api/models.types'

/**
 * ModelCapabilities holds the resolved modality lists used by model badges.
 */
export interface ModelCapabilities {
  /**
   * input and output contain recognized modalities in MODEL_MODALITY_ORDER.
   * resolveModelCapabilities allocates both lists from accepted metadata or from
   * its combined local inference rules.
   */
  input: ModelModality[] // input modality badges
  output: ModelModality[] // output modality badges
}

/**
 * ApiModelCapabilities carries optional supplied modalities before recognized-value
 * filtering and selection.
 */
export interface ApiModelCapabilities {
  /**
   * input and output are considered together after recognized-value filtering.
   * Both sides must remain nonempty to select supplied metadata; otherwise
   * resolveModelCapabilities infers both sides locally.
   */
  input?: ModelModality[] // input modality badges
  output?: ModelModality[] // output modality badges
}

/**
 * MODEL_MODALITY_ORDER fixes recognized modality membership and display order so model
 * cards show comparable input and output badges.
 */
export const MODEL_MODALITY_ORDER: ModelModality[] = ['text', 'image', 'audio', 'video', 'pdf']

/**
 * IMAGE_OUTPUT_PATTERNS identifies model names that replace the default text output
 * with image output during display inference.
 */
const IMAGE_OUTPUT_PATTERNS = [
  /dall-e/,
  /flux/,
  /stable-diffusion/,
  /imagen/,
  /cogview/,
  /midjourney/,
  /mj-/,
  /gpt-image/,
]

/**
 * VIDEO_OUTPUT_PATTERNS identifies model names that add video output and remove the
 * default text output during display inference.
 */
const VIDEO_OUTPUT_PATTERNS = [/veo/, /sora/, /cogvideo/]

/**
 * AUDIO_IO_PATTERNS adds audio to both modality lists and suppresses ordinary
 * vision-name inference for matching model identifiers.
 */
const AUDIO_IO_PATTERNS = [/gpt-4o-audio/, /gpt-audio/, /realtime/]

/**
 * AUDIO_INPUT_PATTERNS replaces text input with audio input and suppresses ordinary
 * vision-name inference for matching model identifiers.
 */
const AUDIO_INPUT_PATTERNS = [/whisper/, /voxtral/, /qwen-audio/]

/**
 * AUDIO_OUTPUT_PATTERNS replaces default text output with audio output and marks
 * matching models as generative for vision inference.
 */
const AUDIO_OUTPUT_PATTERNS = [/tts/]

/**
 * IMAGE_INPUT_PATTERNS supplies vision-name matches after the resolver excludes
 * generative output and audio input or output combinations.
 */
const IMAGE_INPUT_PATTERNS = [
  /claude/,
  /gemini/,
  /gpt-4o/,
  /gpt-4\.1/,
  /gpt-5/,
  /o3/,
  /o4/,
  /qwen-vl/,
  /glm-4v/,
  /pixtral/,
  /llama-4/,
]

/**
 * IMAGE_EDIT_PATTERNS adds image input for editing models even when image generation
 * has already selected their output modalities.
 */
const IMAGE_EDIT_PATTERNS = [/gpt-image/]

/**
 * hasPositivePrice reports whether a supplied amount is finite and positive.
 */
function hasPositivePrice(value?: number): boolean {
  return typeof value === 'number' && Number.isFinite(value) && value > 0
}

/**
 * sortedModalities returns selected recognized modalities in the shared display order.
 */
function sortedModalities(modalities: Set<ModelModality>): ModelModality[] {
  return MODEL_MODALITY_ORDER.filter((modality) => modalities.has(modality))
}

/**
 * sanitizeApiModalities filters supplied modalities into the recognized display order.
 * Non-arrays and empty arrays produce an empty list, duplicates collapse, and unknown
 * values cannot contribute a capability badge.
 */
function sanitizeApiModalities(modalities?: ModelModality[]): ModelModality[] {
  if (!Array.isArray(modalities)) {
    return []
  }
  return MODEL_MODALITY_ORDER.filter((modality) => modalities.includes(modality))
}

/**
 * resolveModelCapabilities chooses supplied modalities or infers model badge values
 * from pricing and identifier patterns. Supplied input and output must both remain
 * nonempty after sanitization; otherwise both sides are inferred together.
 *
 * Inference begins with text on each side. Image, video, and audio rules then modify
 * those sets, while ordinary image-input inference requires a nongenerative, nonaudio
 * model. gpt-image names receive image input through the editing rule even when
 * generation selected image output. Sorted results describe this display heuristic;
 * callers needing execution capabilities must obtain those separately.
 */
export function resolveModelCapabilities(
  modelId: string,
  pricing?: MarketplaceModelPricing,
  apiCapabilities?: ApiModelCapabilities,
): ModelCapabilities {
  /**
   * Sanitize both sides before selecting metadata so a partially populated or wholly
   * invalid side cannot create a half-inferred capability pair.
   */
  const apiInput = sanitizeApiModalities(apiCapabilities?.input)
  const apiOutput = sanitizeApiModalities(apiCapabilities?.output)
  if (apiInput.length > 0 && apiOutput.length > 0) {
    return { input: apiInput, output: apiOutput }
  }

  const id = modelId.toLowerCase()

  /**
   * When metadata is incomplete, text provides the initial display assumption and the
   * following pattern rules refine both modality sets together.
   */
  const input = new Set<ModelModality>(['text'])
  const output = new Set<ModelModality>(['text'])

  const imageOutputByPricing = pricing?.pricing_mode === 'image'
  const imageOutputById = IMAGE_OUTPUT_PATTERNS.some((pattern) => pattern.test(id))
  const audioIO = AUDIO_IO_PATTERNS.some((pattern) => pattern.test(id))
  const audioInput = AUDIO_INPUT_PATTERNS.some((pattern) => pattern.test(id))
  const audioOutput = AUDIO_OUTPUT_PATTERNS.some((pattern) => pattern.test(id))
  const videoOutput = VIDEO_OUTPUT_PATTERNS.some((pattern) => pattern.test(id))

  /**
   * Image generation selects image output and removes the initial text assumption;
   * other matching modality rules can still add further outputs.
   */
  if (imageOutputByPricing || imageOutputById) {
    output.delete('text')
    output.add('image')
  }

  /**
   * Video-name inference removes text output but retains any image or audio output
   * already selected by another matching pattern.
   */
  if (videoOutput) {
    output.delete('text')
    output.add('video')
  }

  if (audioIO) {
    input.add('audio')
    output.add('audio')
  }
  if (audioInput) {
    input.delete('text')
    input.add('audio')
  }
  if (audioOutput) {
    output.delete('text')
    output.add('audio')
  }

  /**
   * Suppress ordinary vision inference for generation and audio patterns, while
   * allowing the dedicated image-editing rule to add image input.
   */
  const generativeOnly = imageOutputByPricing || imageOutputById || videoOutput || audioOutput
  if (IMAGE_EDIT_PATTERNS.some((pattern) => pattern.test(id))) {
    input.add('image')
  } else if (!generativeOnly && !audioIO && !audioInput) {
    const imageInputByPricing = hasPositivePrice(pricing?.image_input_price_per_token)
    const imageInputById = IMAGE_INPUT_PATTERNS.some((pattern) => pattern.test(id))
    if (imageInputByPricing || imageInputById) {
      input.add('image')
    }
  }

  return {
    input: sortedModalities(input),
    output: sortedModalities(output),
  }
}
