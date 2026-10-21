<template>
  <div ref="rootRef" class="settings-tabs" @onboarding-reveal="onTourReveal">
    <div class="settings-tab-list" role="tablist" :aria-label="label">
      <button
        v-for="tab in visibleTabs"
        :id="`${idPrefix}-tab-${tab.key}`"
        :key="tab.key"
        type="button"
        role="tab"
        :aria-selected="activeTab === tab.key"
        :aria-controls="`${idPrefix}-panel-${tab.key}`"
        :tabindex="activeTab === tab.key ? 0 : -1"
        :data-settings-tab-button="tab.key"
        class="settings-tab"
        :class="{ 'settings-tab-active': activeTab === tab.key }"
        @click="selectTab(tab.key)"
        @keydown="onTabKeydown($event, tab.key)"
      >
        {{ tab.label }}
      </button>
    </div>
    <div ref="contentRef" class="settings-tab-content">
      <!-- All supplied panels stay mounted while visibility changes, keeping editor drafts and registration state available when users return to a tab. -->
      <section
        v-for="tab in tabs"
        v-show="isShown(tab.key)"
        v-content-reveal="isShown(tab.key)"
        :id="`${idPrefix}-panel-${tab.key}`"
        :key="tab.key"
        role="tabpanel"
        :aria-labelledby="`${idPrefix}-tab-${tab.key}`"
        :data-settings-tab="tab.key"
        tabindex="0"
        class="space-y-6"
      >
        <slot :name="tab.key" />
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { vContentReveal } from '@/directives/contentReveal'

import { computed, nextTick, ref, watch } from 'vue'

/**
 * SettingsTabs.vue organizes persistent settings panels with keyboard navigation and
 * field reveal helpers. Hidden tabs lose their buttons but retain mounted panels, so
 * editors keep local drafts across tab visibility changes. The active key follows the
 * first visible tab when its current tab disappears.
 *
 * Validation and onboarding can request an element inside another panel. The component
 * activates that panel, emits a reveal event for nested editors, and waits for Vue
 * updates before scrolling or focusing. A post flush watcher resets panel scrolling
 * before this delayed positioning, allowing the requested field position to remain
 * after switching tabs. Native validation reports the first invalid field after this
 * reveal sequence.
 */

/**
 * SettingsTab describes an addressable panel and its corresponding tab button.
 */
export interface SettingsTab {
  key: string /* stable panel identifier */
  label: string /* accessible tab label */

  /**
   * hidden removes the tab button and activation eligibility while its panel stays
   * mounted to retain local editor drafts.
   */
  hidden?: boolean
}

/**
 * props supplies ordered panel identities and labels plus an ID prefix used to connect
 * tab buttons with their panels.
 */
const props = defineProps<{
  idPrefix: string /* panel identifier prefix */
  tabs: SettingsTab[] /* ordered tab definitions */
  label: string /* accessible tab label */
}>()

/**
 * rootRef scopes button and field queries to this tab set so callers can mount
 * multiple independent editors.
 */
const rootRef = ref<HTMLElement | null>(null)
/**
 * contentRef owns the scrolling panel area that resets when the active key changes
 * before field reveal positioning.
 */
const contentRef = ref<HTMLElement | null>(null)
/**
 * visibleTabs filters buttons and activation candidates while the template keeps all
 * supplied panels mounted for draft retention.
 */
const visibleTabs = computed(() => props.tabs.filter((tab) => !tab.hidden))
/**
 * activeTab holds the current panel key and follows the first visible tab when its
 * previous key becomes hidden.
 */
const activeTab = ref(visibleTabs.value[0]?.key ?? '')

/**
 * When the active tab becomes hidden or disappears, move selection to the first
 * visible tab while retaining all supplied panel instances.
 */
watch(visibleTabs, (tabs) => {
  if (!tabs.some((tab) => tab.key === activeTab.value)) activeTab.value = tabs[0]?.key ?? ''
})

/**
 * Reset panel scrolling after Vue patches the selected tab, before revealElement
 * resumes its nextTick positioning for a requested field.
 */
watch(
  activeTab,
  () => {
    if (contentRef.value) contentRef.value.scrollTop = 0
  },
  { flush: 'post' },
)

/**
 * isShown reports whether a panel is active and currently visible.
 */
function isShown(key: string) {
  return activeTab.value === key && visibleTabs.value.some((tab) => tab.key === key)
}

/**
 * findTabButton finds the tab button under this component root. Keys are interpolated
 * into an attribute selector, and a missing root or button produces undefined for
 * optional scrolling and focus.
 */
function findTabButton(key: string) {
  return rootRef.value?.querySelector<HTMLElement>(`[data-settings-tab-button="${key}"]`)
}

/**
 * selectTab changes the active key and scrolls its button into view after Vue updates.
 * Callers supply a visible tab key; field reveal and keyboard handlers coordinate
 * their own focus separately.
 */
async function selectTab(key: string) {
  activeTab.value = key
  /**
   * Wait for tab selection to render before scrolling the button, so its active
   * appearance and the panel scroll reset settle in the same update.
   */
  await nextTick()
  findTabButton(key)?.scrollIntoView?.({ block: 'nearest', inline: 'nearest' })
}

/**
 * onTabKeydown applies wrapped arrow navigation and Home or End selection across
 * visible tabs. Recognized targets prevent the browser default, request selection, and
 * focus the corresponding tab button.
 */
function onTabKeydown(event: KeyboardEvent, key: string) {
  const keys = visibleTabs.value.map((tab) => tab.key)
  const index = keys.indexOf(key)
  let next: string | undefined
  if (event.key === 'ArrowRight') next = keys[(index + 1) % keys.length]
  if (event.key === 'ArrowLeft') next = keys[(index + keys.length - 1) % keys.length]
  if (event.key === 'Home') next = keys[0]
  if (event.key === 'End') next = keys[keys.length - 1]
  if (!next) return
  event.preventDefault()
  void selectTab(next)
  findTabButton(next)?.focus()
}

/**
 * revealElement shows the visible tab containing a requested element, then scrolls and
 * optionally focuses that element. It ignores elements outside visible tab panels. The
 * active key changes before the bubbling form field reveal event, allowing nested
 * editors to expose their content.
 *
 * Waiting for the next Vue tick lets the post flush scroll reset run before final
 * positioning. Focus uses the element itself when it is a control, otherwise its
 * descendant or parent control, with preventScroll retaining the requested scroll
 * position.
 */
async function revealElement(element: HTMLElement, focus = true) {
  const key = element.closest<HTMLElement>('[data-settings-tab]')?.dataset.settingsTab
  if (!key || !visibleTabs.value.some((tab) => tab.key === key)) return
  activeTab.value = key
  element.dispatchEvent(new Event('form-field-reveal', { bubbles: true }))
  /**
   * Allow the post flush panel scroll reset to finish before positioning the field,
   * otherwise the reset could overwrite the requested reveal position.
   */
  await nextTick()
  element.scrollIntoView?.({ block: 'center', inline: 'nearest' })
  if (focus) {
    const selector = 'input, textarea, button, select, [tabindex]'
    /**
     * A field marker can wrap or accompany its control, so focus searches the
     * marker, its descendants, and then its parent for an input target.
     */
    const target = element.matches(selector)
      ? element
      : (element.querySelector<HTMLElement>(selector) ??
        element.parentElement?.querySelector<HTMLElement>(selector))
    target?.focus({ preventScroll: true })
  }
}

/**
 * revealField resolves a selector inside this tab set and delegates element
 * positioning. A selector with no matching element has no effect; callers await
 * completion before reading the revealed panel position.
 */
async function revealField(selector: string) {
  const element = rootRef.value?.querySelector<HTMLElement>(selector)
  if (element) await revealElement(element)
}

/**
 * validate checks participating native controls and reports the first invalid field.
 * It waits for that field reveal before reportValidity, returning false for an invalid
 * field and true when every participating control passes.
 */
async function validate(): Promise<boolean> {
  const fields = rootRef.value?.querySelectorAll<
    HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement
  >('input, textarea, select')

  /**
   * Inspect native validity directly before browser submission, allowing an invalid
   * field in another mounted panel to be revealed before reporting.
   */
  const invalid =
    fields && Array.from(fields).find((field) => field.willValidate && !field.validity.valid)
  if (!invalid) return true
  await revealElement(invalid)
  invalid.reportValidity()
  return false
}

/**
 * onTourReveal positions an HTMLElement targeted by a bubbling onboarding event. It
 * delegates the panel transition to revealElement with focus disabled so the
 * onboarding controller can retain its own focus management.
 */
function onTourReveal(event: Event) {
  if (event.target instanceof HTMLElement) void revealElement(event.target, false)
}

/**
 * The exposed helpers let containing forms select panels, reveal fields, and await
 * native validation while observing the active tab key.
 */
defineExpose({ validate, revealField, selectTab, activeTab })
</script>

<style scoped>
.settings-tabs {
  display: flex;
  height: min(68dvh, 760px);
  flex: 1 1 auto;
  min-height: 0;
  min-width: 0;
  flex-direction: column;
}

.settings-tab-list {
  @apply flex shrink-0 overflow-x-auto border-b border-gray-200 dark:border-dark-700;
}

.settings-tab {
  @apply shrink-0 whitespace-nowrap border-b-2 border-transparent px-4 py-3 text-sm font-medium text-gray-500 transition-colors hover:text-gray-900 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary-500 dark:text-gray-400 dark:hover:text-white;
}

.settings-tab-active {
  @apply border-primary-500 text-primary-700 dark:text-primary-300;
}

.settings-tab-content {
  @apply min-h-0 min-w-0 flex-1 overflow-y-auto overscroll-contain px-1 pb-2 pt-6;
}

/*
 * The narrow layout ends one pixel below the shared 640 pixel small breakpoint,
 * keeping tab padding consistent with responsive form controls.
 */
@media (max-width: 639px) {
  .settings-tab {
    @apply px-3;
  }
}
</style>
