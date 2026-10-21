import { describe, expect, it } from 'vitest'

import { isolateLeavingElement, restoreEnteringElement } from '../leavingElement'

/**
 * leavingElement.spec.ts checks leave-transition form isolation using valid
 * and invalid controls in one root. Assertions distinguish inert interaction
 * blocking from disabled validation state and verify that a cancelled leave
 * restores only the temporarily changed controls.
 */

/**
 * createPanel constructs a detached root with valid, invalid, and button
 * controls. It returns a named lookup helper so tests can compare each control
 * state after transition isolation and restoration.
 */
const createPanel = () => {
  const panel = document.createElement('div')
  panel.innerHTML = `
    <input data-test="valid" value="ok" />
    <input data-test="required" required />
    <button data-test="trigger" type="button">select</button>
  `
  /**
   * get returns a named control from the fixed panel fixture.
   */
  const get = (name: string) =>
    panel.querySelector<HTMLInputElement | HTMLButtonElement>(`[data-test="${name}"]`)!
  return { panel, get }
}

describe('isolateLeavingElement', () => {
  /**
   * A leaving panel contains a valid field, an invalid required field, and a
   * button. Isolation must disable only the invalid field while marking the
   * panel inert.
   */
  it('退出时只禁用校验不通过的控件，其余控件保持可用态外观', () => {
    const { panel, get } = createPanel()
    isolateLeavingElement(panel)

    expect(panel.hasAttribute('inert')).toBe(true)
    expect(get('required').disabled).toBe(true)
    expect(get('valid').disabled).toBe(false)
    expect(get('trigger').disabled).toBe(false)
  })

  /**
   * A panel reenters after its invalid control was temporarily disabled during
   * leave. Restoration must remove inert state and reenable the control owned by
   * that transition.
   */
  it('重新展开时恢复被临时禁用的控件', () => {
    const { panel, get } = createPanel()
    isolateLeavingElement(panel)
    restoreEnteringElement(panel)

    expect(panel.hasAttribute('inert')).toBe(false)
    expect(get('required').disabled).toBe(false)
  })
})
