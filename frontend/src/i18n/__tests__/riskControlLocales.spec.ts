import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zh from '../locales/zh'

// riskControlLocales.spec.ts checks bilingual descriptions of moderation workers
// and pre-block audit key distribution. Assertions preserve distinctions between
// record processing, weighted key selection, and current worker load so
// administrative hints describe the intended controls.

describe('risk control locale copy', () => {
  // Moderation worker hints describe audit and pre-block record processing. Both
  // languages retain that role and exclude observation-task wording that would
  // describe a different worker responsibility.
  it('describes worker runtime as audit and pre-block record processing', () => {
    expect(zh.admin.riskControl.workerStatusHint).toContain('前置拦截记录任务')
    expect(zh.admin.riskControl.workerStatusHint).not.toContain('异步观察任务')
    expect(en.admin.riskControl.workerStatusHint).toContain('pre-block record tasks')
    expect(en.admin.riskControl.workerStatusHint).not.toContain('observation tasks')
  })

  // The pre-block key summary includes worker activity alongside other load
  // values. Both catalogs retain the named worker placeholders so the displayed
  // summary can report active and total workers.
  it('keeps pre-block audit key summary aware of async worker load', () => {
    expect(zh.admin.riskControl.preBlockAPIKeyLoadSummary).toContain(
      'worker：{workerActive} / {workerTotal}',
    )
    expect(en.admin.riskControl.preBlockAPIKeyLoadSummary).toContain(
      'worker: {workerActive} / {workerTotal}',
    )
  })

  // Pre-block audit keys use priority-weighted distribution in the configured
  // description. Both languages describe that selection rule and omit worker-
  // pool wording that would imply a different mechanism.
  it('describes weighted pre-block audit key distribution', () => {
    expect(zh.admin.riskControl.preBlockAPIKeyLoadHint).toContain('按优先级权重分配')
    expect(zh.admin.riskControl.preBlockAPIKeyLoadHint).not.toContain('Worker 池')
    expect(en.admin.riskControl.preBlockAPIKeyLoadHint).toContain('priority-weighted')
    expect(en.admin.riskControl.preBlockAPIKeyLoadHint).not.toContain('worker pool')
  })
})
