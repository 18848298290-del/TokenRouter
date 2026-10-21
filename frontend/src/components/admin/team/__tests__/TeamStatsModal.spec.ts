import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import TeamStatsModal from '../TeamStatsModal.vue'
import type { AdminTeam } from '@/api/admin/teams'
import type { TeamUsageSummary } from '@/api/team'

// TeamStatsModal.spec.ts checks how the team statistics dialog requests totals and
// per-member usage. API spies provide two members and predictable summaries, while a
// chart stub exposes member labels so the test can verify username and email
// presentation.

// The hoisted team API and error spies provide controlled member lists and usage
// summaries for the mounted statistics dialog.
const { members, usage, showError } = vi.hoisted(() => ({
  members: vi.fn(), // Team-member request spy.
  usage: vi.fn(), // Team-usage request spy.
  showError: vi.fn(), // Error notification spy.
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    teams: {
      members, // Team-member request spy.
      usage, // Team-usage request spy.
    }, // Team endpoint substitutes.
  }, // Administrator API substitute.
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError, // Error notification spy.
  }), // Application notification-store substitute.
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key, // Translation key passthrough.
    }), // Translation composable substitute.
  }
})

// team supplies the selected administrator record whose ID must be passed to both
// team-level and member-level usage requests.
const team: AdminTeam = {
  id: 9, // Fixture record identifier.
  name: '统计团队', // Fixture display name.
  status: 'active', // Team availability status.
  member_limit: 10, // Configured membership limit.
  member_count: 1, // Current member count.
  created_at: '2026-07-01T00:00:00Z', // Record creation timestamp.
  updated_at: '2026-07-01T00:00:00Z', // Record update timestamp.
  owner_user_id: 1, // Team owner identifier.
  owner_email: 'owner@example.com', // Team owner email.
}

// summary builds a usage fixture with request counts derived from its cost argument.
const summary = (cost: number): TeamUsageSummary => ({
  actual_cost: cost, // Charged usage amount.
  // Derive whole fixture request counts from the supplied cost.
  request_count: Math.round(cost * 10), // Recorded request count.
  input_tokens: 100, // Input token count.
  output_tokens: 20, // Output token count.
  daily: [
    {
      date: '2026-07-27', // Usage calendar date.
      actual_cost: cost, // Charged usage amount.
      request_count: 1, // Recorded request count.
    },
  ], // Daily usage samples.
})

describe('TeamStatsModal', () => {
  beforeEach(() => {
    members.mockReset()
    usage.mockReset()
    showError.mockReset()
    members.mockResolvedValue([
      {
        user_id: 1, // Owning user identifier.
        username: 'Owner', // User display name.
        email: 'owner@example.com', // User email address.
      },
      {
        user_id: 2, // Owning user identifier.
        username: '', // User display name.
        email: 'member@example.com', // User email address.
      },
    ])
    usage.mockImplementation(
      (
        _teamID: number,
        query?: {
          member_id?: number // Requested member identifier.
        },
      ) => Promise.resolve(summary(query?.member_id ? query.member_id / 10 : 1)),
    )
  })

  // A visible team dialog loads two members and their usage alongside the team total.
  // The assertions check every request target and the username-or-email labels passed
  // to the chart.
  it('加载团队汇总并为每名成员请求独立统计', async () => {
    const wrapper = mount(TeamStatsModal, {
      props: {
        show: true, // Parent-controlled dialog visibility.
        team, // Selected team record.
      }, // Accepted component props.
      global: {
        stubs: {
          BaseDialog: {
            props: ['show', 'title'], // Accepted component props.
            template: '<section v-if="show"><slot /><slot name="footer" /></section>', // Rendered stub markup.
          }, // Dialog rendering substitute.
          BalanceAmount: {
            props: ['amount'], // Accepted component props.
            template: '<span>{{ amount }}</span>', // Rendered stub markup.
          }, // Balance rendering substitute.
          TeamMemberUsageCharts: {
            props: ['series', 'loading'], // Accepted component props.
            template:
              '<div data-test="member-series">{{ series.map(item => item.label).join(",") }}</div>', // Rendered stub markup.
          }, // Member-chart rendering substitute.
        }, // Replaced child components.
      }, // Mount-wide component overrides.
    })

    await flushPromises()

    expect(members).toHaveBeenCalledWith(9)
    expect(usage).toHaveBeenCalledWith(9)
    expect(usage).toHaveBeenCalledWith(9, {
      member_id: 1, // Requested member identifier.
    })
    expect(usage).toHaveBeenCalledWith(9, {
      member_id: 2, // Requested member identifier.
    })
    expect(wrapper.get('[data-test="member-series"]').text()).toBe('Owner,member@example.com')
  })
})
