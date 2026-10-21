import { describe, expect, it } from 'vitest'

import zh from '../locales/zh'

// overviewMiscLocales.spec.ts checks Chinese runtime paths for administrator
// overview, group simulation, and payment messages. The tests guard namespace
// placement and selected visible values so assembling domain fragments exposes
// the labels where their consumers look them up.

describe('overview and payment locale keys', () => {
  // The administrator dashboard requests activity, result, creation, and usage-
  // trend labels at its runtime namespace. The Chinese catalog exposes the
  // expected values at those paths for dashboard rendering.
  it('exposes the missing dashboard copy at the runtime paths', () => {
    expect(zh.admin.dashboard).toMatchObject({
      newUsersToday: '今日新增用户',
      active: '活跃',
      ok: '正常',
      err: '错误',
      create: '创建',
      userUsageTrend: '用户使用趋势（Top 12）',
    })
  })

  // Claude Max usage simulation has a dedicated group configuration namespace.
  // The Chinese catalog keeps its labels there and excludes them from model
  // routing to match the consumer paths.
  it('keeps Claude Max simulation copy outside model routing', () => {
    expect(zh.admin.groups.claudeMaxSimulation).toMatchObject({
      title: 'Claude Max 用量模拟',
      enabled: '已启用（模拟 1h 缓存）',
      disabled: '已禁用',
    })
    expect(zh.admin.groups.modelRouting).not.toHaveProperty('claudeMaxSimulation')
  })

  // Payment administration requests a label for allowing user refunds. The
  // assembled Chinese catalog exposes the expected text at the payment namespace
  // used by that control.
  it('exposes the user refund copy at the runtime path', () => {
    expect(zh.payment.admin.allowUserRefund).toBe('允许用户退款')
  })
})
