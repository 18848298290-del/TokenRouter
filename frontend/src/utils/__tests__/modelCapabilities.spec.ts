import { describe, expect, it } from 'vitest'
import { resolveModelCapabilities } from '@/utils/modelCapabilities'
import type { MarketplaceModelPricing } from '@/api/marketplace.types'

/**
 * modelCapabilities.spec.ts tests supplied model modalities and local display
 * inference. Fixtures cover metadata precedence, invalid and partial modality
 * lists, default text assumptions, generation patterns, image editing, and audio
 * rules that suppress overlapping vision-name matches.
 */

/**
 * textPricing, visionPricing, and imagePricing provide token, image-input-price, and
 * image-billing fixtures that exercise metadata selection against local name inference.
 */
const textPricing: MarketplaceModelPricing = {
  pricing_mode: 'token',
  price_status: 'priced',
}

const visionPricing: MarketplaceModelPricing = {
  pricing_mode: 'token',
  price_status: 'priced',
  image_input_price_per_token: 0.000003,
}

const imagePricing: MarketplaceModelPricing = {
  pricing_mode: 'image',
  price_status: 'priced',
  image_price_1k: 0.5,
}

describe('resolveModelCapabilities', () => {
  /**
   * A generation-model name and image pricing would infer editing support locally.
   * Complete supplied modality lists take precedence, retaining the API-provided text
   * input and image output.
   */
  it('优先使用后端下发的能力元数据，忽略本地规则', () => {
    expect(
      resolveModelCapabilities('gpt-image-2', imagePricing, {
        input: ['text'],
        output: ['image'],
      }),
    ).toEqual({
      input: ['text'],
      output: ['image'],
    })
  })

  /**
   * Supplied modalities contain unsorted known values and an unknown value.
   * Sanitization removes the unknown entry and orders both sides using the shared
   * modality display order.
   */
  it('接口数据按固定顺序输出并过滤非法取值', () => {
    expect(
      resolveModelCapabilities('m1', textPricing, {
        input: ['video', 'text', 'file' as never],
        output: ['audio', 'text'],
      }),
    ).toEqual({
      input: ['text', 'video'],
      output: ['text', 'audio'],
    })
  })

  /**
   * Capability metadata is absent or has no output side for a known vision name. Both
   * calls use local inference and retain text plus image input.
   */
  it('接口数据缺失或单侧为空时回退到本地规则', () => {
    expect(resolveModelCapabilities('claude-sonnet-4-5', textPricing).input).toEqual([
      'text',
      'image',
    ])

    expect(
      resolveModelCapabilities('claude-sonnet-4-5', textPricing, { input: ['text', 'image'] })
        .input,
    ).toEqual(['text', 'image'])
  })

  /**
   * An unrecognized model name has ordinary token pricing and no supplied capabilities.
   * The resolver starts with text input and output for its inferred badge values.
   */
  it('默认按纯文本模型处理', () => {
    expect(resolveModelCapabilities('some-random-model', textPricing)).toEqual({
      input: ['text'],
      output: ['text'],
    })
  })

  /**
   * A vision model is supplied a positive image-input token price. Its resolved input
   * includes text and image, covering that combined pricing-and-name fixture for
   * display inference.
   */
  it('按定价数据识别图片输入', () => {
    expect(resolveModelCapabilities('gpt-5.5', visionPricing).input).toEqual(['text', 'image'])
  })

  /**
   * Known Claude and Gemini names have ordinary token pricing. Both receive image input
   * alongside text through the local vision-name patterns used by capability badges.
   */
  it('按模型 ID 识别已知的视觉模型', () => {
    expect(resolveModelCapabilities('claude-sonnet-4-5', textPricing).input).toEqual([
      'text',
      'image',
    ])
    expect(resolveModelCapabilities('gemini-2.5-pro', textPricing).input).toEqual(['text', 'image'])
  })

  /**
   * Image billing is supplied for a Gemini image model name. The resolver selects image
   * output and retains text input under the generative-model inference rules.
   */
  it('按图片计费模式识别图片输出', () => {
    expect(resolveModelCapabilities('gemini-2.5-flash-image', imagePricing)).toEqual({
      input: ['text'],
      output: ['image'],
    })
  })

  /**
   * A Flux generation name has token pricing rather than image pricing. Name inference
   * still selects image output, covering the generation pattern independently of image
   * billing mode.
   */
  it('定价缺失时按生图模型 ID 兜底识别图片输出', () => {
    expect(resolveModelCapabilities('flux-schnell', textPricing).output).toEqual(['image'])
  })

  /**
   * A gpt-image generation model receives image pricing. The dedicated editing pattern
   * adds image input alongside text while output remains the image modality for this
   * fixture.
   */
  it('识别支持图片编辑的生图模型输入包含图片', () => {
    expect(resolveModelCapabilities('gpt-image-2', imagePricing)).toEqual({
      input: ['text', 'image'],
      output: ['image'],
    })
  })

  /**
   * A DALL-E name has ordinary token pricing and no metadata. Generation inference
   * selects image output and leaves the input list at its default text modality.
   */
  it('纯生图模型不叠加图片输入', () => {
    const capabilities = resolveModelCapabilities('dall-e-3', textPricing)
    expect(capabilities.input).toEqual(['text'])
    expect(capabilities.output).toEqual(['image'])
  })

  /**
   * An audio conversation model name matches the bidirectional audio patterns. Text and
   * audio appear on both sides of the inferred capability pair in fixed order.
   */
  it('识别音频双通的对话模型', () => {
    expect(resolveModelCapabilities('gpt-4o-audio-preview', textPricing)).toEqual({
      input: ['text', 'audio'],
      output: ['text', 'audio'],
    })
  })

  /**
   * A Whisper name matches speech-recognition patterns under ordinary token pricing.
   * Its input becomes audio while output remains text, protecting the removal of
   * default text input.
   */
  it('识别仅音频输入的语音识别模型', () => {
    expect(resolveModelCapabilities('whisper-1', textPricing)).toEqual({
      input: ['audio'],
      output: ['text'],
    })
  })

  /**
   * A TTS name matches speech-synthesis patterns under ordinary token pricing. Its
   * output becomes audio while input remains text, protecting the removal of default
   * text output.
   */
  it('识别仅音频输出的语音合成模型', () => {
    expect(resolveModelCapabilities('tts-1-hd', textPricing)).toEqual({
      input: ['text'],
      output: ['audio'],
    })
  })

  /**
   * Veo and Sora model identifiers match the local video patterns. Both receive video
   * output in place of the initial text output for marketplace capability display.
   */
  it('识别视频生成模型', () => {
    expect(resolveModelCapabilities('veo-3', textPricing).output).toEqual(['video'])
    expect(resolveModelCapabilities('sora-1', textPricing).output).toEqual(['video'])
  })

  /**
   * An audio conversation name also contains the gpt-4o vision fragment. The audio
   * guard suppresses ordinary image-input inference so the model does not gain an image
   * badge.
   */
  it('音频对话模型不按 gpt-4o 模式误判图片输入', () => {
    expect(resolveModelCapabilities('gpt-4o-audio-preview', textPricing).input).not.toContain(
      'image',
    )
  })
})
