import { defineComponent } from 'vue'
import { shallowMount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import RequestDetailCard from '../RequestDetailCard.vue'
import requests from '@/i18n/locales/en/requests'
import type { RequestDetail } from '@/api/requests'

vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({
    t: (key: string) => {
      const value = key.split('.').reduce<unknown>((node, part) =>
        node && typeof node === 'object' ? (node as Record<string, unknown>)[part] : undefined,
      { requests })
      return typeof value === 'string' ? value : key
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
  // Distinct header and response identities must remain labeled with their provenance.
  // A repeated ID from another attempt must retain its own provider attribution.
  it('shows header source, response classification and attempt ownership', () => {
    const item = detail()
    item.attempts = [{ number: 1, provider_id: 7, upstream_request_id: 'chosen', upstream_request_id_header: 'X-Client-Request-Id' }]
    item.aliases = [
      { kind: 'caller', value: 'client', source: 'X-Client-Request-ID' },
      { kind: 'upstream_request', value: 'chosen', source: 'X-Client-Request-Id', attempt_number: 1, provider_id: 7 },
      { kind: 'response', value: 'resp_result', source: 'response.id', attempt_number: 1, provider_id: 7 },
      { kind: 'upstream_request', value: 'chosen', source: 'X-Request-Id', attempt_number: 2, provider_id: 8 }
    ]
    const wrapper = renderDetail(item)
    expect(wrapper.text()).toContain('Caller request ID')
    expect(wrapper.text()).toContain('Upstream request ID')
    expect(wrapper.text()).toContain('Response ID')
    expect(wrapper.text()).toContain('X-Client-Request-Id')
    expect(wrapper.text()).toContain('response.id')
    expect(wrapper.text()).toContain('#8 · #2')
    expect(wrapper.findAll('dl').at(-1)!.findAll('dd')).toHaveLength(4)
  })

  // Pre-dispatch failures have no HTTP ordinal but retain their selected providers.
  // Rendering must keep both entries visible without presenting them as a numbered exchange.
  it('shows multiple unnumbered provider failures', () => {
    const item = detail()
    item.attempts = [
      { number: 0, provider_id: 7, outcome: 'failed', stage: 'provider_auth', reason: 'first failure' },
      { number: 0, provider_id: 8, outcome: 'failed', stage: 'provider_auth', reason: 'second failure' }
    ]
    const wrapper = renderDetail(item)
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
})
