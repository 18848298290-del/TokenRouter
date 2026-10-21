import { ref, nextTick, watch } from 'vue'
import { useRoute } from 'vue-router'

/**
 * useNavigationSettings.ts owns the navigation controls and actions within
 * the administrator settings page. Its state is created during page setup and
 * remains available while tabs are hidden, so draft controls and asynchronous
 * actions share the page lifetime.
 */

/**
 * SettingsTab identifies each top-level administrator settings tab and route query value.
 */
export type SettingsTab =
  | 'general'
  | 'agreement'
  | 'features'
  | 'security'
  | 'users'
  | 'gateway'
  | 'payment'
  | 'email'
  | 'backup'

/**
 * GatewaySection identifies each gateway platform group displayed beneath the settings tabs.
 */
export type GatewaySection =
  'general' | 'anthropic' | 'openai' | 'grok' | 'antigravity' | 'ollamaCloud'
/**
 * useNavigationSettings creates page-local tab selection and keyboard navigation.
 * Call it during setup so route query initialization and watchers share the page
 * lifetime; bind its element refs to the root navigation and gateway containers.
 */
export function useNavigationSettings() {
  /**
   * Route state and tab definitions determine the initial selection, button order, and keyboard destinations for this page.
   */
  const route = useRoute()

  const settingsTabs = [
    { key: 'general' as SettingsTab, icon: 'home' as const },
    { key: 'agreement' as SettingsTab, icon: 'document' as const },
    { key: 'features' as SettingsTab, icon: 'bolt' as const },
    { key: 'security' as SettingsTab, icon: 'shield' as const },
    { key: 'users' as SettingsTab, icon: 'user' as const },
    { key: 'gateway' as SettingsTab, icon: 'server' as const },
    { key: 'payment' as SettingsTab, icon: 'creditCard' as const },
    { key: 'email' as SettingsTab, icon: 'mail' as const },
    { key: 'backup' as SettingsTab, icon: 'database' as const },
  ]

  const settingsTabKeys = new Set<SettingsTab>(settingsTabs.map((tab) => tab.key))

  const initialSettingsTab = settingsTabKeys.has(route.query.tab as SettingsTab)
    ? (route.query.tab as SettingsTab)
    : 'general'

  const activeTab = ref<SettingsTab>(initialSettingsTab)

  const settingsTabsScrollRef = ref<HTMLElement | null>(null)

  const gatewaySections = [
    { key: 'general' as GatewaySection },
    { key: 'anthropic' as GatewaySection, providerBrand: 'Anthropic' },
    { key: 'openai' as GatewaySection, providerBrand: 'OpenAI' },
    { key: 'grok' as GatewaySection, providerBrand: 'Grok' },
    { key: 'antigravity' as GatewaySection, providerBrand: 'Google' },
    { key: 'ollamaCloud' as GatewaySection, providerBrand: 'Ollama' },
  ]

  const activeGatewaySection = ref<GatewaySection>('general')

  const gatewaySectionsScrollRef = ref<HTMLElement | null>(null)

  const gatewayContentStartRef = ref<HTMLElement | null>(null)

  /**
   * Arrow keys advance through the current tab list, while Home and End select its ordered endpoints.
   */
  const settingsTabKeyboardActions = {
    ArrowLeft: -1,
    ArrowUp: -1,
    ArrowRight: 1,
    ArrowDown: 1,
    Home: 'first',
    End: 'last',
  } as const

  /**
   * selectSettingsTab activates the requested top-level administrator settings tab.
   */
  function selectSettingsTab(tab: SettingsTab): void {
    activeTab.value = tab
  }

  /**
   * focusSettingsTab schedules focus on the requested top-level tab button.
   * The animation-frame callback runs after selection updates; if the page has
   * removed that button, the optional lookup leaves focus unchanged.
   */
  function focusSettingsTab(tab: SettingsTab): void {
    window.requestAnimationFrame(() => {
      document.getElementById(`settings-tab-${tab}`)?.focus()
    })
  }

  /**
   * handleSettingsTabKeydown applies arrow, Home, and End navigation to settings tabs.
   * Recognized keys prevent browser scrolling, wrap within the ordered tab list,
   * and schedule focus on the newly selected button.
   */
  function handleSettingsTabKeydown(event: KeyboardEvent, tab: SettingsTab): void {
    const action = settingsTabKeyboardActions[event.key as keyof typeof settingsTabKeyboardActions]
    if (action === undefined) {
      return
    }

    event.preventDefault()
    const currentIndex = settingsTabs.findIndex((item) => item.key === tab)
    let nextIndex = currentIndex < 0 ? 0 : currentIndex

    if (action === 'first') {
      nextIndex = 0
    } else if (action === 'last') {
      nextIndex = settingsTabs.length - 1
    } else {
      nextIndex = (nextIndex + action + settingsTabs.length) % settingsTabs.length
    }

    const nextTab = settingsTabs[nextIndex]?.key
    if (!nextTab) {
      return
    }

    selectSettingsTab(nextTab)
    focusSettingsTab(nextTab)
  }

  /**
   * selectGatewaySection activates a gateway platform and scrolls its content into view.
   * The scroll runs after Vue updates the visible cards, using the root content
   * anchor and its sticky-navigation scroll margin.
   */
  function selectGatewaySection(section: GatewaySection): void {
    activeGatewaySection.value = section
    void nextTick(() => {
      gatewayContentStartRef.value?.scrollIntoView?.({ block: 'start' })
    })
  }

  /**
   * focusGatewaySection schedules focus on the requested gateway platform button.
   * Selection updates occur before the animation-frame lookup, and a missing
   * button leaves the current document focus unchanged for that callback.
   */
  function focusGatewaySection(section: GatewaySection): void {
    window.requestAnimationFrame(() => {
      document.getElementById(`gateway-section-tab-${section}`)?.focus()
    })
  }

  /**
   * handleGatewaySectionKeydown applies the shared tab keyboard actions to gateway platforms.
   * Recognized keys prevent browser scrolling, wrap through the configured section
   * order, and focus the section selected by the key.
   */
  function handleGatewaySectionKeydown(event: KeyboardEvent, section: GatewaySection): void {
    const action = settingsTabKeyboardActions[event.key as keyof typeof settingsTabKeyboardActions]
    if (action === undefined) {
      return
    }

    event.preventDefault()
    const currentIndex = gatewaySections.findIndex((item) => item.key === section)
    let nextIndex = currentIndex < 0 ? 0 : currentIndex

    if (action === 'first') {
      nextIndex = 0
    } else if (action === 'last') {
      nextIndex = gatewaySections.length - 1
    } else {
      nextIndex = (nextIndex + action + gatewaySections.length) % gatewaySections.length
    }

    const nextSection = gatewaySections[nextIndex]?.key
    if (!nextSection) {
      return
    }

    selectGatewaySection(nextSection)
    focusGatewaySection(nextSection)
  }

  /**
   * scrollTabHorizontallyIntoView centers a selected button within its scroll container.
   * Missing containers or buttons are ignored while loading hides the navigation.
   * It changes horizontal scroll only and uses direct scrollLeft assignment when
   * the browser does not expose the scrollTo method.
   */
  function scrollTabHorizontallyIntoView(
    container: HTMLElement | null,
    activeSelector: string,
  ): void {
    const tabElement = container?.querySelector<HTMLElement>(activeSelector)
    if (!container || !tabElement) {
      return
    }

    const containerRect = container.getBoundingClientRect()
    const tabRect = tabElement.getBoundingClientRect()
    const nextScrollLeft = Math.max(
      0,
      container.scrollLeft +
        tabRect.left -
        containerRect.left -
        (container.clientWidth - tabElement.clientWidth) / 2,
    )

    if (typeof container.scrollTo === 'function') {
      container.scrollTo({ left: nextScrollLeft, behavior: 'smooth' })
      return
    }

    // Environments without scrollTo still reveal the selected tab by assigning
    // the same computed horizontal position directly to the container.
    container.scrollLeft = nextScrollLeft
  }

  /**
   * scrollActiveSettingsTabIntoView centers the selected top-level tab after Vue updates.
   * Call it after loading or selection changes; an unmounted navigation container
   * is accepted because the shared scrolling helper ignores missing elements.
   */
  function scrollActiveSettingsTabIntoView() {
    void nextTick(() => {
      scrollTabHorizontallyIntoView(settingsTabsScrollRef.value, '.settings-tab-active')
    })
  }

  /**
   * Selection watchers run after rendering so active button classes and conditional gateway navigation exist before horizontal scrolling.
   */
  watch(activeTab, scrollActiveSettingsTabIntoView, {
    immediate: true,
    flush: 'post',
  })

  /**
   * scrollActiveGatewaySectionIntoView centers the active gateway section after Vue updates.
   * It schedules scrolling while the Gateway tab is selected, allowing hidden
   * section navigation to retain its position until the user returns.
   */
  function scrollActiveGatewaySectionIntoView(): void {
    if (activeTab.value !== 'gateway') {
      return
    }

    void nextTick(() => {
      scrollTabHorizontallyIntoView(gatewaySectionsScrollRef.value, '.gateway-section-tab-active')
    })
  }

  watch([activeTab, activeGatewaySection], scrollActiveGatewaySectionIntoView, {
    immediate: true,
    flush: 'post',
  })
  return {
    route,
    settingsTabs,
    settingsTabKeys,
    initialSettingsTab,
    activeTab,
    settingsTabsScrollRef,
    gatewaySections,
    activeGatewaySection,
    gatewaySectionsScrollRef,
    gatewayContentStartRef,
    settingsTabKeyboardActions,
    selectSettingsTab,
    focusSettingsTab,
    handleSettingsTabKeydown,
    selectGatewaySection,
    focusGatewaySection,
    handleGatewaySectionKeydown,
    scrollTabHorizontallyIntoView,
    scrollActiveSettingsTabIntoView,
    scrollActiveGatewaySectionIntoView,
  }
}
