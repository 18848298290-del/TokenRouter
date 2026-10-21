import type { DOMWrapper } from '@vue/test-utils'

// switches.ts reads and changes boolean form controls through their
// accessible switch state. Tests use aria-checked to determine the current
// value and trigger the control click when a requested value differs.

// isSwitchOn reports whether the supplied wrapper exposes aria-checked as
// true.
export function isSwitchOn(target: DOMWrapper<Element>): boolean {
  return target.attributes('aria-checked') === 'true'
}

// setSwitch clicks a switch when its accessible state differs from the
// requested value. Callers await the returned promise for the triggered Vue
// update; matching states complete without a click.
export async function setSwitch(target: DOMWrapper<Element>, value: boolean): Promise<void> {
  if (isSwitchOn(target) !== value) await target.trigger('click')
}
