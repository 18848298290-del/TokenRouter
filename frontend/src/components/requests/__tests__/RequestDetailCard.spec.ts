import { defineComponent } from 'vue'
import { shallowMount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import RequestDetailCard from '../RequestDetailCard.vue'
import requests from '@/i18n/locales/en/requests'
import type { RequestDetail } from '@/api/requests'

vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({
    t: (key: string, params?: unknown) => {
      const value = key.split('.').reduce<unknown>((node, part) =>
        node && typeof node === 'object' ? (node as Record<string, unknown>)[part] : undefined,
      { requests })
      if (typeof value !== 'string') return key
      // Named placeholders such as {source} are filled the way vue-i18n fills them.
      const named = params && typeof params === 'object' ? params as Record<string, unknown> : {}
      return value.replace(/\{(\w+)\}/g, (match, name: string) => name in named ? String(named[name]) : match)
    }
  })
}))

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({ formatBalanceAmount: (value: number) => String(value) })
}))

// renderDetail mounts diagnostic identity rows with real translations and visible ID values.
function renderDetail(item: RequestDetail) {
  return shallowMount(RequestDetailCard, {
    props: { item },
    global: {
      stubs: {
        RequestIdLink: defineComponent({ props: ['value'], template: '<span>{{ value }}</span>' })
      }
    }
  })
}

// detail provides one completed request with no unrelated usage or error content.
function detail(): RequestDetail {
  return { request_id: 'local', started_at: '2026-10-17T10:00:00Z', method: 'POST', path: '/v1/responses', state: 'completed', legacy: false, usage: [], errors: [] }
}

describe('RequestDetailCard identifiers', () => {
  // Each attempt shows the upstream request ID with its header and the response ID it owns.
  // Related identifiers keep only the rest, including an ID whose attempt is not listed.
  it('shows owned identifiers on their attempt and lists each identifier once', () => {
    const item = detail()
    item.attempts = [{ number: 1, provider_id: 7, upstream_request_id: 'chosen', upstream_request_id_header: 'X-Client-Request-Id' }]
    item.aliases = [
      { kind: 'caller', value: 'client', source: 'X-Client-Request-ID' },
      { kind: 'upstream_request', value: 'chosen', source: 'X-Client-Request-Id', attempt_number: 1, provider_id: 7 },
      { kind: 'response', value: 'resp_result', source: 'response.id', attempt_number: 1, provider_id: 7 },
      { kind: 'upstream_request', value: 'chosen', source: 'X-Request-Id', attempt_number: 2, provider_id: 8 }
    ]
    const wrapper = renderDetail(item)
    const [attemptDetails, related] = wrapper.findAll('dl').slice(-2)
    expect(attemptDetails.text()).toContain('Upstream request ID')
    expect(attemptDetails.text()).toContain('from X-Client-Request-Id')
    expect(attemptDetails.text()).toContain('Response ID')
    expect(attemptDetails.text()).toContain('resp_result')
    expect(related.findAll('dd')).toHaveLength(2)
    expect(related.text()).toContain('Caller request ID')
    expect(related.text()).toContain('from X-Client-Request-ID')
    expect(related.text()).toContain('from X-Request-Id · Provider #8 · Attempt #2')
    expect(related.text()).not.toContain('resp_result')
  })

  // Pre-dispatch failures have no HTTP ordinal but retain their selected providers.
  // Rendering must keep both entries visible with a readable stage, without presenting them as a numbered exchange.
  it('shows multiple unnumbered provider failures', () => {
    const item = detail()
    item.attempts = [
      { number: 0, provider_id: 7, outcome: 'failed', stage: 'provider_auth', reason: 'first failure' },
      { number: 0, provider_id: 8, outcome: 'failed', stage: 'provider_auth', reason: 'second failure' }
    ]
    const wrapper = renderDetail(item)
    expect(wrapper.text()).toContain('Provider authentication error')
    expect(wrapper.text()).toContain('first failure')
    expect(wrapper.text()).toContain('second failure')
    expect(wrapper.text()).toContain('Unnumbered')
    expect(wrapper.text()).not.toContain('#0')
  })

  // Historical identifiers lack reliable provenance even when their spelling resembles Responses.
  // Rendering must retain the generic label and explicitly report the missing source.
  it('does not infer historical response identity from a prefix', () => {
    const item = detail()
    item.aliases = [{ kind: 'upstream', value: 'resp_historical' }]
    const wrapper = renderDetail(item)
    expect(wrapper.text()).toContain('Upstream identifier (unclassified)')
    expect(wrapper.text()).toContain('Source not recorded')
    expect(wrapper.text()).not.toContain('Response ID')
    expect(wrapper.text()).toContain('resp_historical')
  })

  // A request can carry failure stages and usage performance at once.
  // A lone failure timeline names the section, while several timelines each get a caption.
  it('titles one failure timeline and captions several timelines', () => {
    const item = detail()
    item.failure_timings = { upstream_started_ms: 12 }
    expect(renderDetail(item).find('section h3').text()).toBe('Failure timing')

    item.usage = [{ id: 3, model: 'model-a', input_tokens: 1, output_tokens: 1, actual_cost: 0, timings: { ttft_mode: 'first_output', upstream_stream: true, values: { attempt_number: 2, upstream_started_ms: 40 } } }]
    const wrapper = renderDetail(item)
    expect(wrapper.find('section h3').text()).toBe('Timing')
    expect(wrapper.text()).toContain('Failure timing')
    expect(wrapper.text()).toContain('model-a · Attempt #2')
  })
})
