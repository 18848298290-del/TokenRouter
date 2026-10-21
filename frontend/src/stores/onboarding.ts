import { defineStore } from 'pinia'
import { markRaw, ref, shallowRef } from 'vue'
import type { Driver } from 'driver.js'

// onboarding.ts shares the active tour driver and callbacks registered by onboarding
// components. The driver remains a raw shallow reference across component consumers,
// while callback setters let the tour owner install or clear navigation controls. Calls
// made before registration return their documented empty result or perform no action.

// VoidCallback represents a registered tour replay operation with no arguments.
type VoidCallback = () => void
// TeamGuideOptions supplies membership context for choosing the team onboarding steps.
export interface TeamGuideOptions {
  isOwner: boolean // Current team ownership status.
  hasTeam: boolean // Current team membership status.
}

// TeamGuideCallback starts the team tour using current ownership and membership context.
type TeamGuideCallback = (options: TeamGuideOptions) => void
// NextStepCallback advances a tour after an optional delay measured in milliseconds.
type NextStepCallback = (delay?: number) => Promise<void>
// IsCurrentStepCallback matches the active tour target against a component selector.
type IsCurrentStepCallback = (selector: string) => boolean

// useOnboardingStore exposes a shared tour driver and registration methods for
// component-owned callbacks. Tour owners install controls during setup and clear them
// when their lifecycle ends; callers can use empty defaults before registration.
export const useOnboardingStore = defineStore('onboarding', () => {
  // Callback refs belong to the registered tour owner; other components invoke them
  // through store actions that handle missing registrations.
  const replayCallback = ref<VoidCallback | null>(null)
  const teamGuideCallback = ref<TeamGuideCallback | null>(null)
  const nextStepCallback = ref<NextStepCallback | null>(null)
  const isCurrentStepCallback = ref<IsCurrentStepCallback | null>(null)

  // driverInstance shares the raw driver across components; its creating owner remains
  // responsible for driver destruction and resource cleanup.
  const driverInstance = shallowRef<Driver | null>(null)

  // setReplayCallback registers or clears the callback used to replay the onboarding
  // tour.
  function setReplayCallback(callback: VoidCallback | null): void {
    replayCallback.value = callback
  }

  // setTeamGuideCallback registers or clears the callback that starts the team tour.
  function setTeamGuideCallback(callback: TeamGuideCallback | null): void {
    teamGuideCallback.value = callback
  }

  // setControlMethods installs the tour owner's navigation and step-matching callbacks.
  // The owner supplies both controls together and calls clearControlMethods when they
  // should stop receiving requests from other components.
  function setControlMethods(methods: {
    nextStep: NextStepCallback // Registered tour advancement operation.
    isCurrentStep: IsCurrentStepCallback // Registered target matching operation.
  }): void {
    nextStepCallback.value = methods.nextStep
    isCurrentStepCallback.value = methods.isCurrentStep
  }

  // clearControlMethods removes both registered controls from the shared onboarding
  // store.
  function clearControlMethods(): void {
    nextStepCallback.value = null
    isCurrentStepCallback.value = null
  }

  // setDriverInstance stores the tour driver as a raw shared reference, or clears it
  // with null. The caller retains responsibility for destroying the driver and its
  // browser resources.
  function setDriverInstance(driver: Driver | null): void {
    driverInstance.value = driver ? markRaw(driver) : null
  }

  // getDriverInstance returns the shared raw tour driver, or null before registration
  // and after clearing. Consumers receive the same mutable driver reference; its
  // creating owner remains responsible for destruction and browser resource cleanup.
  function getDriverInstance(): Driver | null {
    return driverInstance.value
  }

  // isDriverActive reports whether the stored driver's optional isActive method
  // reports an active tour. An absent driver or method returns false, allowing
  // callers to check activity before registration and after the owner clears it.
  function isDriverActive(): boolean {
    return driverInstance.value?.isActive?.() ?? false
  }

  // replay invokes the registered tour callback when one is available. Calls before
  // registration or after the owner clears the callback perform no action and leave
  // shared state unchanged.
  function replay(): void {
    if (replayCallback.value) {
      replayCallback.value()
    }
  }

  // startTeamGuide passes the current team ownership and membership state to the
  // registered callback. Calls before registration perform no action, allowing team
  // pages to invoke the entry point safely.
  function startTeamGuide(options: TeamGuideOptions): void {
    teamGuideCallback.value?.(options)
  }

  // nextStep awaits the registered navigation callback with an optional delay in
  // milliseconds. The delay defaults to zero, and calls before registration resolve
  // immediately without advancing a tour.
  async function nextStep(delay = 0): Promise<void> {
    if (nextStepCallback.value) {
      await nextStepCallback.value(delay)
    }
  }

  // isCurrentStep asks the registered control whether its highlighted element matches
  // the selector. Missing controls return false, allowing components outside an active
  // tour to skip tour-specific behavior.
  function isCurrentStep(selector: string): boolean {
    if (isCurrentStepCallback.value) {
      return isCurrentStepCallback.value(selector)
    }
    return false
  }

  return {
    setReplayCallback,
    setTeamGuideCallback,
    setControlMethods,
    clearControlMethods,
    setDriverInstance,
    getDriverInstance,
    isDriverActive,
    replay,
    startTeamGuide,
    nextStep,
    isCurrentStep,
  }
})
