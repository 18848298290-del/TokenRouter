import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import ModelAttributeDetails from '../ModelAttributeDetails.vue'
import type { MarketplaceModel } from '@/api/marketplace.types'

// ModelAttributeDetails.spec.ts checks protocol selection in mounted marketplace
// attribute panels. Fixtures provide differing route capabilities, allowing the
// tests to inspect union versus protocol views, unknown evidence, keyboard
// dismissal, legacy responses, and independently initialized model panels.

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  // useI18n replaces component translation while retaining the actual module exports.
  useI18n: () => ({
    // t exposes translation keys so assertions identify the requested labels.
    t: (key: string) => key,
  }),
}))

// model supplies a public alias with disjoint text and image protocol attributes.
// Its route summaries deliberately differ in ceilings and tool support so one
// protocol selection exercises every displayed attribute family together.
function model(): MarketplaceModel {
  return {
    id: 'alias',
    display_name: 'Grok',
    pricing: { pricing_mode: 'token' },
    protocols: ['openai_responses', 'openai_images_generations'],
    native_protocols: ['openai_responses'],
    attributes: {
      output_modalities: ['text', 'image'],
      input_modalities: ['text', 'image'],
      output_limit: 200,
      tool_call: true,
      route_differences: true,
    },
    protocol_attributes: {
      openai_responses: {
        output_modalities: ['text'],
        input_modalities: ['text', 'image'],
        output_limit: 200,
        tool_call: true,
        reasoning_options: [{ type: 'effort', values: ['low', 'high'] }],
      },
      openai_images_generations: {
        output_modalities: ['image'],
        input_modalities: ['text'],
        output_limit: 50,
        tool_call: false,
        media: { image_sizes: ['1K'] },
      },
    },
  }
}

// panel returns the teleported interactive details for assertions against real controls.
// Callers open the mounted interactive fixture first; static legacy responses
// expose a tooltip and are inspected separately.
function panel(): HTMLElement {
  return document.body.querySelector<HTMLElement>('[role="dialog"]')!
}

// choose activates a protocol inside the panel and waits for reactive rendering.
// Callers open the fixture first and supply one of its advertised protocols
// or the all-protocol reset identifier.
async function choose(protocol: string) {
  panel()
    .querySelector<HTMLButtonElement>(
      `[data-protocol="${protocol}"] button, button[data-protocol="${protocol}"]`,
    )!
    .click()
  await nextTick()
}

describe('ModelAttributeDetails', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    vi.restoreAllMocks()
  })

  // This scenario compares the full union with two protocol selections. Every
  // attribute family switches together while the overall modality icons retain
  // their positions and the public price object remains unchanged.
  it('switches complete attributes and dims unavailable modalities without replacing icons', async () => {
    const value = model()
    const price = value.pricing
    const wrapper = mount(ModelAttributeDetails, {
      props: { model: value },
      attachTo: document.body,
    })
    await wrapper.get('button').trigger('click')
    expect(panel().querySelector('[data-protocol="all"]')?.getAttribute('aria-pressed')).toBe(
      'true',
    )
    expect(panel().querySelectorAll('[data-modality^="output-"]')).toHaveLength(2)
    await choose('openai_images_generations')
    expect(panel().querySelector('[data-modality="output-text"]')?.getAttribute('data-state')).toBe(
      'unsupported',
    )
    expect(
      panel().querySelector('[data-modality="output-image"]')?.getAttribute('data-state'),
    ).toBe('supported')
    expect(panel().querySelector('[data-capability="tool_call"]')?.getAttribute('data-state')).toBe(
      'unsupported',
    )
    expect(panel().textContent).toContain('50')
    expect(panel().textContent).toContain('1K')
    await choose('openai_responses')
    expect(
      panel().querySelector('[data-modality="output-image"]')?.getAttribute('data-state'),
    ).toBe('unsupported')
    expect(panel().textContent).toContain('200')
    expect(panel().textContent).toContain('low, high')
    expect(panel().textContent).not.toContain('1K')
    expect(
      panel().querySelector('[data-protocol="openai_responses"]')?.getAttribute('data-native'),
    ).toBe('true')
    expect(value.pricing).toBe(price)
    await choose('all')
    expect(
      panel().querySelector('[data-modality="output-image"]')?.getAttribute('data-state'),
    ).toBe('supported')
    wrapper.unmount()
  })

  // Missing route evidence must remain visibly unknown after selecting a protocol.
  // Closing through Escape restores focus and reopening starts from all protocols.
  it('marks unknown evidence and resets selection after keyboard dismissal', async () => {
    const value = model()
    value.protocol_attributes!.openai_responses = {
      variants: [{}, { output_modalities: [] }],
      output_modalities: [],
    }
    const wrapper = mount(ModelAttributeDetails, {
      props: { model: value },
      attachTo: document.body,
    })
    const trigger = wrapper.get('button')
    await trigger.trigger('keydown', { key: 'ArrowDown' })
    await nextTick()
    expect(document.activeElement).toBe(panel().querySelector('button'))
    await choose('openai_responses')
    expect(
      panel().querySelector('[data-modality="output-image"]')?.getAttribute('data-state'),
    ).toBe('unknown')
    expect(panel().textContent).toContain('admin.modelAttributes.unknown')
    panel().querySelector('button')!.focus()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await nextTick()
    expect(document.activeElement).toBe(trigger.element)
    await trigger.trigger('click')
    expect(panel().querySelector('[data-protocol="all"]')?.getAttribute('aria-pressed')).toBe(
      'true',
    )
    wrapper.unmount()
  })

  // Older public responses contain protocol labels without protocol attribute maps.
  // They must keep their readable summary and expose no misleading selector controls.
  it('keeps static protocol labels for responses without protocol attributes', async () => {
    const value = model()
    delete value.protocol_attributes
    const wrapper = mount(ModelAttributeDetails, {
      props: { model: value },
      attachTo: document.body,
    })
    await wrapper.get('button').trigger('click')
    const legacy = document.body.querySelector('[role="tooltip"]')!
    expect(legacy.querySelector('[data-protocol="all"]')).toBeNull()
    expect(legacy.querySelector('[data-protocol="openai_responses"] button')).toBeNull()
    expect(legacy.textContent).toContain('200')
    wrapper.unmount()
  })

  // The first model panel selects its image protocol before a second panel opens.
  // The second panel must begin with its own all-protocol selection.
  it('isolates selection between model panels', async () => {
    const one = mount(ModelAttributeDetails, { props: { model: model() }, attachTo: document.body })
    const two = mount(ModelAttributeDetails, {
      props: { model: { ...model(), id: 'other' } },
      attachTo: document.body,
    })
    await one.get('button').trigger('click')
    await choose('openai_images_generations')
    await two.get('button').trigger('click')
    const panels = document.body.querySelectorAll('[role="dialog"]')
    expect(panels[1].querySelector('[data-protocol="all"]')?.getAttribute('aria-pressed')).toBe(
      'true',
    )
    one.unmount()
    two.unmount()
  })
})
