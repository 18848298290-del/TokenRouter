import { onMounted, onUnmounted, nextTick } from 'vue'
import { driver, type Driver, type DriveStep } from 'driver.js'
import 'driver.js/dist/driver.css'
import { useAuthStore as useUserStore } from '@/stores/auth'
import { useOnboardingStore, type TeamGuideOptions } from '@/stores/onboarding'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import {
  getAdminSteps,
  getTeamSteps,
  getUserSteps,
  type RoutedDriveStep,
} from '@/composables/onboarding/steps'

// useOnboardingTour.ts coordinates driver.js onboarding with routed pages,
// interactive controls, and the shared onboarding store. Steps can require a
// route change, a visible target, or a successful form action before advancing.
// A click listener remembers the step that installed it and waits for the
// interaction to render before checking that an active driver reports the
// captured step index.
//
// The onboarding store keeps the driver across layout instances and exposes
// form-driven advancement. Step replacement removes local listeners and restores
// target focus attributes. Driver destruction owns the global keyboard listener,
// while component unmount clears its auto-start timer and registered control
// methods. Seen markers are scoped to the user, role, and tour version in
// browser storage.

// OnboardingOptions configures persisted tour identity and automatic
// administrator startup for a mounted layout.
export interface OnboardingOptions {
  storageKey?: string // Optional seen-marker key prefix.
  autoStart?: boolean // Enable automatic administrator startup.
}

// useOnboardingTour registers routed and interactive tour controls during
// component setup. It requires the router, authentication store, onboarding
// store, and translations from the current Vue application. Driver lifetime is
// shared through the store, so component unmount clears its timer and control
// methods while driver destruction removes the global keyboard handler. Callers
// use nextStep after successful form actions; interactive target listeners
// compare the active index with their bound index after a delay.
export function useOnboardingTour(options: OnboardingOptions) {
  const { t } = useI18n()
  const userStore = useUserStore()
  const onboardingStore = useOnboardingStore()
  const router = useRouter()
  const storageVersion = 'v4_interactive' // Default tour completion namespace version.
  const teamStorageVersion = 'v1_routed'

  // Timing limits allow interaction feedback, routed target rendering, and
  // delayed administrator startup to complete before the next tour action.
  const TIMING = {
    INTERACTIVE_WAIT_MS: 800, // Default wait time for interactive steps
    ELEMENT_TIMEOUT_MS: 8000, // Timeout for element detection
    AUTO_START_DELAY_MS: 1000, // Delay before auto-starting tour
  } as const

  // Helper: Check if a step is interactive (only close button shown)
  // isInteractiveStep reports whether a step exposes only its close button.
  const isInteractiveStep = (step: DriveStep): boolean => {
    return step.popover?.showButtons?.length === 1 && step.popover.showButtons[0] === 'close'
  }

  // Helper: Clean up click listener
  // cleanupClickListener removes events installed for the active target and
  // restores its previous tabindex. Calling it repeatedly is safe because the
  // tracked listener record is cleared after cleanup.
  const cleanupClickListener = () => {
    if (!currentClickListener) return
    const { element: el, handler, keyHandler, originalTabIndex, eventTypes } = currentClickListener
    if (eventTypes) {
      eventTypes.forEach((type) => el.removeEventListener(type, handler))
    }
    if (keyHandler) el.removeEventListener('keydown', keyHandler)
    if (originalTabIndex !== undefined) {
      if (originalTabIndex === null) el.removeAttribute('tabindex')
      else el.setAttribute('tabindex', originalTabIndex)
    }
    currentClickListener = null
  }

  // The driver reference follows shared store ownership; target listeners and
  // the auto-start timer belong to this composable instance.
  let driverInstance: Driver | null = onboardingStore.getDriverInstance()
  let currentClickListener: {
    element: HTMLElement // Current highlighted target element.
    handler: () => void // Installed target interaction callback.
    keyHandler?: (e: KeyboardEvent) => void // Optional keyboard interaction callback.
    originalTabIndex?: string | null // Target tabindex before binding.
    eventTypes?: string[] // Track which event types were added
  } | null = null
  let autoStartTimer: ReturnType<typeof setTimeout> | null = null
  let globalKeyboardHandler: ((e: KeyboardEvent) => void) | null = null

  // getStorageKey builds a seen-marker key from the configured prefix, user, role,
  // and tour version. Anonymous sessions use guest identity, and absent role uses
  // the personal user role.
  const getStorageKey = () => {
    const baseKey = options.storageKey ?? 'onboarding_tour'
    const userId = userStore.user?.id ?? 'guest'
    const role = userStore.user?.role ?? 'user'
    return `${baseKey}_${userId}_${role}_${storageVersion}`
  }

  // hasSeen reports whether the current user and role completed this tour.
  const hasSeen = () => {
    return localStorage.getItem(getStorageKey()) === 'true'
  }

  // markAsSeen writes the current user and role tour completion marker.
  const markAsSeen = () => {
    localStorage.setItem(getStorageKey(), 'true')
  }

  // clearSeen removes the current user and role tour completion marker.
  const clearSeen = () => {
    localStorage.removeItem(getStorageKey())
  }

  // markTeamGuideAsSeen writes the current user's versioned team guide completion
  // marker.
  const markTeamGuideAsSeen = () => {
    const userId = userStore.user?.id ?? 'guest'
    localStorage.setItem(`team_guide_${userId}_${teamStorageVersion}`, 'true')
  }

  // ensureElement waits for a selector to resolve to an element with positive
  // height. It polls until the supplied timeout and returns false when a visible
  // target never appears.
  const ensureElement = async (selector: string, timeout = 5000): Promise<boolean> => {
    const startTime = Date.now()
    while (Date.now() - startTime < timeout) {
      const element = document.querySelector(selector)
      if (element && element.getBoundingClientRect().height > 0) {
        return true
      }
      // Poll briefly so asynchronously mounted or revealed targets can become
      // visible before the tour timeout expires.
      await new Promise((resolve) => setTimeout(resolve, 150))
    }
    return false
  }

  // startTour creates a driver for the selected administrator, personal, or team
  // flow. It waits for initial DOM readiness, replaces an existing driver, and
  // registers target and keyboard callbacks. Routed movement waits for the
  // destination page before locating its element, while interactive advancement
  // remembers its bound step across the delay. Completion marks the selected flow
  // as seen before driver destruction removes listeners and clears shared driver
  // ownership.
  const startTour = async (
    startIndex = 0,
    flow: 'default' | 'team' = 'default',
    teamOptions?: TeamGuideOptions,
  ) => {
    // Resolve current role and membership when starting so the tour uses the
    // controls available to this user now.
    const isAdmin = userStore.user?.role === 'admin'
    const steps: RoutedDriveStep[] =
      flow === 'team'
        ? getTeamSteps(t, teamOptions?.isOwner ?? false, teamOptions?.hasTeam ?? false)
        : isAdmin
          ? getAdminSteps(t)
          : getUserSteps(t)
    const driverSteps = steps.map(({ route: _route, ...step }) => step)
    const markCurrentTourAsSeen = flow === 'team' ? markTeamGuideAsSeen : markAsSeen

    // Wait for pending Vue rendering before searching for the first target so
    // mounted form controls can be located.
    await nextTick()

    // Allow the requested starting target to become visible before creating the
    // driver and its initial highlight overlay.
    const currentStep = steps[startIndex]
    if (currentStep?.element && typeof currentStep.element === 'string') {
      await ensureElement(currentStep.element, TIMING.ELEMENT_TIMEOUT_MS)
    }

    // Destroy the previous driver before publishing its replacement so its target
    // and keyboard listeners are released through the existing destruction callback.
    if (driverInstance) {
      driverInstance.destroy()
    }

    // Create the driver with callbacks that coordinate navigation, target
    // interaction, and publication through the shared onboarding store.
    driverInstance = driver({
      showProgress: true,
      steps: driverSteps,
      animate: true,
      allowClose: false, // Overlay clicks cannot dismiss.
      stagePadding: 4,
      popoverClass: 'theme-tour-popover',
      nextBtnText: t('common.next'),
      prevBtnText: t('common.back'),
      doneBtnText: t('common.confirm'),

      // Driver navigation callbacks share completion marking and target routing.
      onNextClick: async (_el, _step, { config, state }) => {
        // Persist completion before destroying the final step so a subsequent
        // layout mount can observe the finished tour.
        if (state.activeIndex === (config.steps?.length ?? 0) - 1) {
          markCurrentTourAsSeen()
          driverInstance?.destroy()
          onboardingStore.setDriverInstance(null)
        } else {
          // If Next is invoked for an interactive control, trigger that control
          // and let its own interaction advance the tour.
          const currentIndex = state.activeIndex ?? 0
          const currentStep = steps[currentIndex]

          if (currentStep && isInteractiveStep(currentStep) && currentStep.element) {
            const targetElement =
              typeof currentStep.element === 'string'
                ? (document.querySelector(currentStep.element) as HTMLElement)
                : (currentStep.element as HTMLElement)

            if (targetElement) {
              const isClickable = !['INPUT', 'TEXTAREA', 'SELECT'].includes(targetElement.tagName)
              if (isClickable) {
                targetElement.click()
                return
              }
            }
          }
          await moveToStep(currentIndex + 1)
        }
      },
      onPrevClick: async (_el, _step, { state }) => {
        await moveToStep((state.activeIndex ?? 0) - 1)
      },
      onCloseClick: () => {
        markCurrentTourAsSeen()
        driverInstance?.destroy()
        onboardingStore.setDriverInstance(null)
      },

      // Interactive popovers append a keyboard hint while keeping the driver
      // navigation buttons and progress presentation supplied by its template.
      onPopoverRender: (popover, { state }) => {
        try {
          const currentStep = steps[state.activeIndex ?? 0]

          if (currentStep && isInteractiveStep(currentStep) && popover.description) {
            const hintClass = 'driver-popover-description-hint'
            if (!popover.description.querySelector(`.${hintClass}`)) {
              const hint = document.createElement('div')
              hint.className = `${hintClass} mt-2 text-xs text-gray-500 flex items-center gap-1`

              const iconSpan = document.createElement('span')
              iconSpan.className = 'i-mdi-keyboard-return mr-1'

              const textNode = document.createTextNode(
                t('onboarding.interactiveHint', 'Press Enter or Click to continue'),
              )

              hint.appendChild(iconSpan)
              hint.appendChild(textNode)
              popover.description.appendChild(hint)
            }
          }
        } catch (e) {
          // Keep driver navigation available when optional hint rendering fails,
          // and retain a diagnostic for the failed DOM update.
          console.error('Onboarding Tour Render Error:', e)
        }
      },

      // Highlight callbacks prepare target visibility and install interaction listeners.
      onHighlightStarted: async (element, step) => {
        // Release the previous target listeners and restore its tab index before
        // binding interactions to the newly highlighted target.
        cleanupClickListener()

        // A routed or conditionally rendered target may arrive after
        // highlighting starts, so wait for visibility before attaching its
        // interaction handlers.
        if (!element && step.element && typeof step.element === 'string') {
          const exists = await ensureElement(step.element, 8000)
          if (!exists) {
            console.warn(`Tour element not found after 8s: ${step.element}`)
            return
          }
          element = document.querySelector(step.element) as HTMLElement
        }

        // Reveal the target through its enclosing form sections, then refresh
        // the driver after Vue renders the expanded or selected panel.
        if (element) {
          element.dispatchEvent(new Event('onboarding-reveal', { bubbles: true }))
          await nextTick()
          driverInstance?.refresh()
        }

        if (isInteractiveStep(step) && element) {
          const htmlElement = element as HTMLElement

          // Check if this is a submit button - if so, don't bind auto-advance listeners
          // Let business code (e.g., handleCreateGroup) manually call nextStep after success
          const isSubmitButton =
            htmlElement.getAttribute('type') === 'submit' ||
            (htmlElement.tagName === 'BUTTON' && htmlElement.closest('form'))

          if (isSubmitButton) {
            return // Don't bind any click listeners for submit buttons
          }

          const originalTabIndex = htmlElement.getAttribute('tabindex')
          if (!htmlElement.isContentEditable && htmlElement.tabIndex === -1) {
            htmlElement.setAttribute('tabindex', '0')
          }

          // Enhanced Select component detection - check both children and self
          const isSelectComponent =
            htmlElement.querySelector('.select-trigger') !== null ||
            htmlElement.classList.contains('select-trigger')

          // Select dropdowns are teleported to <body>, so click events on options
          // won't bubble through this element. Skip auto-advance for Select components.
          // Users navigate using Next/Previous buttons after making their selection.
          if (isSelectComponent) {
            return
          }

          // Single-execution protection flag
          let hasExecuted = false

          // Capture the step index when binding the handler
          const boundStepIndex = driverInstance?.getActiveIndex() ?? 0

          // clickHandler accepts the bound target's first activation and delays
          // advancement so its action can display feedback. It sets hasExecuted
          // before waiting, preventing click, keyboard, or input events sharing
          // this closure from scheduling another transition. After the delay,
          // the current driver must be active and report the captured step index.
          // This check compares indexes, so a replacement driver at the same
          // index can still pass and receive the requested next-step transition.
          const clickHandler = async () => {
            // Prevent duplicate execution
            if (hasExecuted) {
              return
            }
            hasExecuted = true

            // Delay advancement until the target action has time to render
            // feedback, then recheck the bound step before moving.
            await new Promise((resolve) => setTimeout(resolve, TIMING.INTERACTIVE_WAIT_MS))

            // Verify driver is still active and not destroyed
            if (!driverInstance || !driverInstance.isActive()) {
              return
            }

            // Check if we're still on the same step - abort if step changed during wait
            const currentIndex = driverInstance.getActiveIndex() ?? 0
            if (currentIndex !== boundStepIndex) {
              return
            }

            // Final check before moving
            if (driverInstance && driverInstance.isActive()) {
              await moveToStep(currentIndex + 1)
            }
          }

          // For input fields, advance on input/change events instead of click
          const isInputField = ['INPUT', 'TEXTAREA', 'SELECT'].includes(htmlElement.tagName)

          if (isInputField) {
            // inputHandler removes the target's input and change listeners before
            // requesting delayed advancement. The first event therefore consumes
            // both subscriptions, while clickHandler applies its shared one-shot
            // and active-index checks to the resulting transition.
            const inputHandler = () => {
              // Remove both input event subscriptions before delaying
              // advancement, so a change event cannot schedule another
              // transition for this target.
              htmlElement.removeEventListener('input', inputHandler)
              htmlElement.removeEventListener('change', inputHandler)
              clickHandler()
            }

            htmlElement.addEventListener('input', inputHandler)
            htmlElement.addEventListener('change', inputHandler)

            currentClickListener = {
              element: htmlElement,
              handler: inputHandler,
              originalTabIndex,
              eventTypes: ['input', 'change'],
            }
          } else {
            // keyHandler activates the bound tour interaction for Enter or Space.
            const keyHandler = (e: KeyboardEvent) => {
              if (['Enter', ' '].includes(e.key)) {
                e.preventDefault()
                clickHandler()
              }
            }

            htmlElement.addEventListener('click', clickHandler, { once: true })
            htmlElement.addEventListener('keydown', keyHandler)

            currentClickListener = {
              element: htmlElement,
              handler: clickHandler as () => void,
              keyHandler,
              originalTabIndex,
              eventTypes: ['click'],
            }
          }
        }
      },

      onDestroyed: () => {
        cleanupClickListener()
        // Driver destruction owns keyboard cleanup so a tour kept across layout
        // unmounts retains its shortcuts until it actually closes.
        if (globalKeyboardHandler) {
          document.removeEventListener('keydown', globalKeyboardHandler, { capture: true })
          globalKeyboardHandler = null
        }
        onboardingStore.setDriverInstance(null)
      },
    })

    onboardingStore.setDriverInstance(driverInstance)

    // The capture listener routes navigation keys while leaving editing keys
    // available inside supported form controls.
    globalKeyboardHandler = (e: KeyboardEvent) => {
      if (!driverInstance?.isActive()) return

      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopPropagation()
        markCurrentTourAsSeen()
        driverInstance.destroy()
        onboardingStore.setDriverInstance(null)
        return
      }

      if (e.key === 'ArrowRight') {
        const target = e.target as HTMLElement
        // Let the focused input retain arrow keys for editing rather than moving
        // the tour while the user changes field contents.
        if (['INPUT', 'TEXTAREA'].includes(target?.tagName)) {
          return
        }

        e.preventDefault()
        e.stopPropagation()

        // Interactive controls decide their own advancement; inspect the active
        // target before allowing a navigation key to move the tour.
        const currentIndex = driverInstance!.getActiveIndex() ?? 0
        const currentStep = steps[currentIndex]

        if (currentStep && isInteractiveStep(currentStep) && currentStep.element) {
          const targetElement =
            typeof currentStep.element === 'string'
              ? (document.querySelector(currentStep.element) as HTMLElement)
              : (currentStep.element as HTMLElement)

          if (targetElement) {
            // Clickable interactive targets wait for click or Enter before advancing.
            const isClickable = !['INPUT', 'TEXTAREA', 'SELECT'].includes(targetElement.tagName)
            if (isClickable) {
              // Leave this target active for explicit interaction.
              return
            }
          }
        }

        // Advance ordinary informational steps with the right arrow key.
        void moveToStep((driverInstance!.getActiveIndex() ?? 0) + 1)
      } else if (e.key === 'Enter') {
        const target = e.target as HTMLElement
        // Leave Enter with the focused input so its form or editor can process
        // the key without tour navigation intercepting it.
        if (['INPUT', 'TEXTAREA'].includes(target?.tagName)) {
          return
        }

        e.preventDefault()
        e.stopPropagation()

        // Activate the interactive target with Enter before considering normal tour advancement.
        const currentIndex = driverInstance!.getActiveIndex() ?? 0
        const currentStep = steps[currentIndex]

        if (currentStep && isInteractiveStep(currentStep) && currentStep.element) {
          const targetElement =
            typeof currentStep.element === 'string'
              ? (document.querySelector(currentStep.element) as HTMLElement)
              : (currentStep.element as HTMLElement)

          if (targetElement) {
            const isClickable = !['INPUT', 'TEXTAREA', 'SELECT'].includes(targetElement.tagName)
            if (isClickable) {
              targetElement.click()
              return
            }
          }
        }
        void moveToStep((driverInstance!.getActiveIndex() ?? 0) + 1)
      } else if (e.key === 'ArrowLeft') {
        const target = e.target as HTMLElement
        // Let the focused input retain arrow keys for editing rather than moving
        // the tour while the user changes field contents.
        if (
          ['INPUT', 'TEXTAREA', 'SELECT'].includes(target?.tagName) ||
          target?.isContentEditable
        ) {
          return
        }

        e.preventDefault()
        e.stopPropagation()
        void moveToStep((driverInstance.getActiveIndex() ?? 0) - 1)
      }
    }

    document.addEventListener('keydown', globalKeyboardHandler, { capture: true })
    driverInstance.drive(startIndex)

    // moveToStep navigates to the target step route before waiting for its
    // visible element. Missing steps and inactive drivers return immediately.
    // Route changes are awaited before DOM lookup so selectors resolve in the
    // destination page. A missing target leaves the current step active and logs
    // a warning; a target found later advances only while the driver remains
    // active after those asynchronous waits.
    async function moveToStep(targetIndex: number): Promise<void> {
      const targetStep = steps[targetIndex]
      if (!targetStep || !driverInstance?.isActive()) return

      if (targetStep.route) {
        const targetRoute = router.resolve(targetStep.route)
        if (router.currentRoute.value.fullPath !== targetRoute.fullPath) {
          await router.push(targetStep.route)
          await nextTick()
        }
      }

      if (targetStep.element && typeof targetStep.element === 'string') {
        const exists = await ensureElement(targetStep.element, TIMING.ELEMENT_TIMEOUT_MS)
        if (!exists) {
          console.warn(`Onboarding: Target step element not found: ${targetStep.element}`)
          return
        }
      }

      if (driverInstance?.isActive()) driverInstance.moveTo(targetIndex)
    }
  }

  // nextStep advances the active driver after an optional delay in milliseconds.
  // Form handlers call it after successful actions; its initial active check
  // returns immediately when no tour is running.
  const nextStep = async (delay = 300) => {
    if (!driverInstance?.isActive()) return
    if (delay > 0) {
      await new Promise((resolve) => setTimeout(resolve, delay))
    }
    driverInstance.moveNext()
  }

  // isCurrentStep reports whether the active highlighted element matches the
  // supplied selector.
  const isCurrentStep = (elementSelector: string): boolean => {
    if (!driverInstance?.isActive()) return false
    const activeElement = driverInstance.getActiveElement()
    return activeElement?.matches(elementSelector) ?? false
  }

  // replayTour clears the current seen marker before scheduling the default role
  // flow again. It returns immediately after starting asynchronous setup; the
  // cleared marker remains cleared if setup cannot start the tour.
  const replayTour = () => {
    clearSeen()
    void startTour()
  }

  // startTeamTour starts the team flow with membership and ownership supplied by
  // its caller. The operation schedules asynchronous setup, while the shared store
  // keeps the resulting driver available to form controls.
  const startTeamTour = (teamOptions: TeamGuideOptions) => {
    void startTour(0, 'team', teamOptions)
  }

  onMounted(async () => {
    onboardingStore.setControlMethods({
      nextStep,
      isCurrentStep,
    })

    if (onboardingStore.isDriverActive()) {
      driverInstance = onboardingStore.getDriverInstance()
      return
    }

    // Automatic startup is restricted to administrators; personal and team tours
    // remain available through their explicit replay controls.
    const isAdmin = userStore.user?.role === 'admin'
    if (!isAdmin) {
      return
    }

    if (!options.autoStart || hasSeen()) return
    autoStartTimer = setTimeout(() => {
      void startTour()
    }, TIMING.AUTO_START_DELAY_MS)
  })

  onUnmounted(() => {
    if (autoStartTimer) {
      clearTimeout(autoStartTimer)
      autoStartTimer = null
    }
    // Driver destruction owns the global keyboard listener; unmount releases
    // this layout's control methods while the shared driver may remain active.
    onboardingStore.clearControlMethods()
  })

  return {
    startTour,
    replayTour,
    startTeamTour,
    nextStep,
    isCurrentStep,
    hasSeen,
    markAsSeen,
    clearSeen,
  }
}
