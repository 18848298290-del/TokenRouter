<template>
  <SettingsTabs
    ref="tabsRef"
    :id-prefix="idPrefix"
    :tabs="tabs"
    :label="t('admin.groups.tabs.label')"
  >
    <template #general>
      <SettingsSection :title="t('admin.groups.tabs.identity')">
        <div class="grid gap-4 md:grid-cols-2">
          <div data-group-field="name">
            <label :for="`${idPrefix}-name`" class="input-label">{{
              t('admin.groups.form.name')
            }}</label>
            <input
              :id="`${idPrefix}-name`"
              v-model="form.name"
              type="text"
              required
              class="input"
              :placeholder="t('admin.groups.enterGroupName')"
              :data-tour="mode === 'create' ? 'group-form-name' : 'edit-group-form-name'"
            />
          </div>
          <div>
            <label :for="`${idPrefix}-brand`" class="input-label">{{
              t('admin.groups.form.displayBrand')
            }}</label>
            <Select
              :aria-label="t('admin.groups.form.displayBrand')"
              :id="`${idPrefix}-brand`"
              v-model="form.display_brand"
              :options="providerBrandOptions"
              :placeholder="t('admin.groups.displayBrandPlaceholder')"
              :search-placeholder="t('admin.groups.displayBrandPlaceholder')"
              :creatable-prefix="t('admin.groups.displayBrandCreatablePrefix')"
              searchable
              creatable
            />
          </div>
          <div class="md:col-span-2">
            <LocalizedFieldsEditor
              :model-value="form.localization"
              :source-locale="mode === 'create' ? locale || 'en' : null"
              :source="{ display_name: form.name, description: form.description }"
              :fields="[
                { key: 'display_name', label: t('localization.displayName') },
                {
                  key: 'description',
                  label: t('admin.groups.form.description'),
                  placeholder: t('admin.groups.optionalDescription'),
                  multiline: true,
                },
              ]"
              @update:model-value="
                form.localization = $event;
                form.description = $event.source.description;
              "
            />
          </div>
        </div>
      </SettingsSection>
      <SettingsSection :title="t('admin.groups.settings.billingAndStatus')">
        <div class="grid gap-4 md:grid-cols-2">
          <div>
            <label :for="`${idPrefix}-rate-multiplier`" class="input-label">{{
              t('admin.groups.form.rateMultiplier')
            }}</label>
            <input
              :id="`${idPrefix}-rate-multiplier`"
              v-model.number="form.rate_multiplier"
              type="number"
              step="0.001"
              min="0.001"
              required
              class="input"
              data-tour="group-form-multiplier"
            />
            <p class="input-hint">{{ t('admin.groups.rateMultiplierHint') }}</p>
          </div>
          <div v-if="mode === 'edit'">
            <label :for="`${idPrefix}-status`" class="input-label">{{
              t('admin.groups.form.status')
            }}</label>
            <Select
              :aria-label="t('admin.groups.form.status')"
              :id="`${idPrefix}-status`"
              v-model="form.status"
              :options="statusOptions"
            />
          </div>
        </div>
        <SettingToggleRow
          :id="`${idPrefix}-exclusive`"
          v-model="form.is_exclusive"
          :label="t('admin.groups.form.exclusive')"
          :hint="t(form.is_exclusive ? 'admin.groups.exclusive' : 'admin.groups.public')"
          :help="`${t('admin.groups.exclusiveTooltip.description')} ${t('admin.groups.exclusiveTooltip.exampleContent')}`"
          setting="is_exclusive"
          data-tour="group-form-exclusive"
        />
      </SettingsSection>
    </template>

    <template #models>
      <GroupRoutingPolicyFields :id-prefix="idPrefix" v-model="form.routing_policy" />
      <GroupModelRoutingFields
        :id-prefix="idPrefix"
        :enabled="form.model_routing_enabled"
        :rules="routingRules"
        :search="providerSearch"
        :get-key="getRuleKey"
        @enabled="form.model_routing_enabled = $event"
        @add="emit('addRule')"
        @remove="emit('removeRule', $event)"
        @pattern="(rule, value) => emit('rulePattern', rule, value)"
        @search="(rule, keyword) => emit('searchProviders', rule, keyword)"
        @focus="emit('focusProviders', $event)"
        @select-provider="(rule, provider) => emit('selectProvider', rule, provider)"
        @remove-provider="(rule, id) => emit('removeProvider', rule, id)"
      />
      <GroupModelsListFields
        :id-prefix="idPrefix"
        :state="modelsList"
        :loading="modelsListLoading"
        @enabled="emit('modelsEnabled', $event)"
        @select="(id, value) => emit('selectModel', id, value)"
        @select-all="emit('selectAllModels')"
        @invert="emit('invertModels')"
        @move="(from, to) => emit('moveModel', from, to)"
      />
    </template>

    <template #scheduling>
      <SettingsSection :title="t('admin.groups.settings.providerSelection')">
        <div v-if="options.copyProviders.length">
          <div class="mb-2 flex items-center gap-2">
            <label :for="`${idPrefix}-copy-providers`" class="input-label mb-0">{{
              t('admin.groups.copyProviders.title')
            }}</label>
            <HelpTooltip
              :content="t('admin.groups.copyProviders.tooltip')"
              :tooltip-id="`${idPrefix}-copy-help`"
              trigger="both"
              :closable="false"
            >
              <template #trigger>
                <button
                  type="button"
                  class="inline-flex rounded-compact text-gray-400 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500"
                  :aria-label="t('admin.groups.copyProviders.title')"
                  :aria-describedby="`${idPrefix}-copy-help`"
                >
                  <Icon name="questionCircle" size="sm" />
                </button>
              </template>
            </HelpTooltip>
          </div>
          <div v-if="form.copy_providers_from_group_ids.length" class="mb-2 flex flex-wrap gap-2">
            <span
              v-for="groupId in form.copy_providers_from_group_ids"
              :key="groupId"
              class="inline-flex max-w-full items-center gap-2 rounded-compact bg-primary-100 px-2 py-1 text-xs text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
            >
              <span class="min-w-0 break-all">{{ copyGroupLabel(groupId) }}</span>
              <button
                type="button"
                class="shrink-0 rounded-compact focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500"
                :aria-label="
                  t('admin.groups.settings.removeItem', {
                    name: copyGroupLabel(groupId),
                  })
                "
                @click="
                  form.copy_providers_from_group_ids = form.copy_providers_from_group_ids.filter(
                    (id) => id !== groupId,
                  )
                "
              >
                <Icon name="x" size="sm" />
              </button>
            </span>
          </div>
          <Select
            :aria-label="t('admin.groups.form.status')"
            :id="`${idPrefix}-copy-providers`"
            :model-value="null"
            :options="copyOptions"
            :placeholder="t('admin.groups.copyProviders.selectPlaceholder')"
            @change="addCopyGroup"
          />
          <p class="input-hint">{{ t('admin.groups.copyProviders.hint') }}</p>
        </div>
        <SettingToggleRow
          :id="`${idPrefix}-oauth`"
          v-model="form.require_oauth_only"
          :label="t('admin.groups.providerFilters.oauthOnly')"
          :hint="t('admin.groups.settings.oauthHint')"
          setting="require_oauth_only"
        />
        <SettingToggleRow
          :id="`${idPrefix}-privacy`"
          v-model="form.require_privacy_set"
          :label="t('admin.groups.providerFilters.privacyRequired')"
          :hint="t('admin.groups.settings.privacyHint')"
          setting="require_privacy_set"
        />
      </SettingsSection>
      <SettingsSection :title="t('admin.groups.settings.scheduling')">
        <div>
          <label :for="`${idPrefix}-scheduler`" class="input-label">{{
            t('admin.groups.form.schedulerType')
          }}</label>
          <Select
            :aria-label="t('admin.groups.form.schedulerType')"
            :id="`${idPrefix}-scheduler`"
            v-model="form.scheduler_type"
            :options="schedulerOptions"
          />
          <p class="input-hint">{{ t('admin.groups.scheduler.hint') }}</p>
          <div
            v-if="form.scheduler_type === 'advanced'"
            class="mt-4 flex flex-wrap items-center justify-between gap-3 rounded-surface border border-gray-200 bg-gray-50/50 p-4 dark:border-dark-600 dark:bg-dark-800/40"
          >
            <div class="min-w-0">
              <span class="text-sm font-medium text-primary-900 dark:text-dark-50">{{
                t('admin.groups.advancedSchedulerOverrides.label')
              }}</span>
              <p class="input-hint">{{ advancedSummary }}</p>
            </div>
            <button type="button" class="btn btn-secondary" @click="emit('configureScheduler')">
              <Icon name="cog" size="sm" />{{
                t('admin.groups.advancedSchedulerOverrides.configure')
              }}
            </button>
          </div>
        </div>
        <SettingToggleRow
          :id="`${idPrefix}-session-isolation`"
          v-model="form.session_isolation_enabled"
          :label="t('admin.groups.sessionIsolation.title')"
          :hint="t('admin.groups.sessionIsolation.hint')"
          setting="session_isolation_enabled"
        />
      </SettingsSection>
      <SettingsSection :title="t('admin.groups.settings.fallbacks')">
        <div class="grid gap-4 md:grid-cols-2">
          <div>
            <label :for="`${idPrefix}-unavailable-fallback`" class="input-label">{{
              t('admin.groups.unavailableFallback.title')
            }}</label>
            <Select
              :aria-label="t('admin.groups.form.schedulerType')"
              :id="`${idPrefix}-unavailable-fallback`"
              v-model="form.unavailable_fallback_group_id"
              :options="options.unavailableFallback"
              :placeholder="t('admin.groups.unavailableFallback.noFallback')"
            />
            <p class="input-hint">
              {{ t('admin.groups.unavailableFallback.hint') }}
            </p>
          </div>
          <div>
            <label :for="`${idPrefix}-invalid-fallback`" class="input-label">{{
              t('admin.groups.invalidRequestFallback.title')
            }}</label>
            <Select
              :aria-label="t('admin.groups.invalidRequestFallback.title')"
              :id="`${idPrefix}-invalid-fallback`"
              v-model="form.fallback_group_id_on_invalid_request"
              :options="options.invalidRequestFallback"
              :placeholder="t('admin.groups.invalidRequestFallback.noFallback')"
            />
            <p class="input-hint">
              {{ t('admin.groups.invalidRequestFallback.hint') }}
            </p>
          </div>
        </div>
      </SettingsSection>
      <SettingsSection data-group-field="probe">
        <SettingToggleRow
          :id="`${idPrefix}-probe`"
          v-model="form.availability_probe_enabled"
          :label="t('admin.groups.availabilityProbe.title')"
          :hint="t('admin.groups.availabilityProbe.hint')"
          setting="availability_probe_enabled"
        />
        <div
          v-if="form.availability_probe_enabled"
          class="grid gap-4 rounded-surface border border-gray-200 bg-gray-50/50 p-4 dark:border-dark-600 dark:bg-dark-800/40 md:grid-cols-2"
        >
          <div>
            <label :for="`${idPrefix}-probe-model`" class="input-label">{{
              t('admin.groups.availabilityProbe.model')
            }}</label>
            <Select
              :aria-label="t('admin.groups.availabilityProbe.model')"
              :id="`${idPrefix}-probe-model`"
              v-model="form.availability_probe_model_id"
              data-group-field="probe-model"
              :options="options.probeModels"
              searchable
            />
          </div>
          <div>
            <label :for="`${idPrefix}-probe-interval`" class="input-label">{{
              t('admin.groups.availabilityProbe.interval')
            }}</label>
            <input
              :id="`${idPrefix}-probe-interval`"
              v-model.number="form.availability_probe_interval_minutes"
              type="number"
              min="1"
              max="1440"
              class="input"
            />
          </div>
          <div>
            <label :for="`${idPrefix}-probe-timeout`" class="input-label">{{
              t('admin.groups.availabilityProbe.timeout')
            }}</label>
            <input
              :id="`${idPrefix}-probe-timeout`"
              v-model.number="form.availability_probe_timeout_seconds"
              type="number"
              min="5"
              max="120"
              class="input"
            />
          </div>
          <div>
            <label :for="`${idPrefix}-probe-retries`" class="input-label">{{
              t('admin.groups.availabilityProbe.maxRetries')
            }}</label>
            <input
              :id="`${idPrefix}-probe-retries`"
              v-model.number="form.availability_probe_max_retries"
              type="number"
              min="0"
              max="10"
              step="1"
              class="input"
            />
          </div>
          <div class="md:col-span-2">
            <label :for="`${idPrefix}-probe-agent`" class="input-label">{{
              t('admin.groups.availabilityProbe.userAgent')
            }}</label>
            <input
              :id="`${idPrefix}-probe-agent`"
              v-model="form.availability_probe_user_agent"
              type="text"
              maxlength="512"
              class="input"
              :placeholder="t('admin.groups.availabilityProbe.userAgentPlaceholder')"
            />
          </div>
          <div class="md:col-span-2">
            <label :for="`${idPrefix}-probe-prompt`" class="input-label">{{
              t('admin.groups.availabilityProbe.prompt')
            }}</label>
            <textarea
              :id="`${idPrefix}-probe-prompt`"
              v-model="form.availability_probe_prompt"
              data-group-field="probe-prompt"
              rows="3"
              class="input"
              :placeholder="t('admin.groups.availabilityProbe.promptPlaceholder')"
            />
          </div>
        </div>
      </SettingsSection>
    </template>

    <template #protocol>
      <GroupClientProtocolSelector
        :id-prefix="idPrefix"
        v-model="form.allowed_protocols"
        v-model:fallbacks="form.protocol_fallbacks"
        v-model:image-policy="form.responses_image_policy"
      />
      <SettingsSection>
        <SettingToggleRow
          :id="`${idPrefix}-claude-code`"
          v-model="form.claude_code_only"
          :label="t('admin.groups.claudeCode.title')"
          :hint="
            t(
              form.claude_code_only
                ? 'admin.groups.claudeCode.enabled'
                : 'admin.groups.claudeCode.disabled',
            )
          "
          :help="t('admin.groups.claudeCode.tooltip')"
          setting="claude_code_only"
        />
        <div v-if="form.claude_code_only">
          <label :for="`${idPrefix}-client-fallback`" class="input-label">{{
            t('admin.groups.claudeCode.fallbackGroup')
          }}</label>
          <Select
            :aria-label="t('admin.groups.claudeCode.fallbackGroup')"
            :id="`${idPrefix}-client-fallback`"
            v-model="form.fallback_group_id"
            :options="options.clientFallback"
            :placeholder="t('admin.groups.claudeCode.noFallback')"
          />
          <p class="input-hint">
            {{ t('admin.groups.claudeCode.fallbackHint') }}
          </p>
        </div>
      </SettingsSection>
    </template>

    <template #request>
      <SettingsSection
        :title="t('admin.groups.openaiFast.title')"
        :data-testid="`${mode}-openai-fast`"
      >
        <Select
          v-model="form.openai_fast_policy"
          data-setting="openai_fast_policy"
          :aria-label="t('admin.groups.openaiFast.policy')"
          :options="fastOptions"
        />
        <p class="input-hint">{{ t('admin.groups.openaiFast.hint') }}</p>
      </SettingsSection>
      <SettingsSection :title="t('admin.groups.settings.reasoning')">
        <ReasoningEffortPolicyFields
          ref="reasoningRef"
          data-group-field="reasoning"
          :id-prefix="`${idPrefix}-reasoning`"
          v-model:max-effort="form.max_reasoning_effort"
          v-model:over-limit="form.max_reasoning_effort_over_limit"
          v-model:mappings="form.reasoning_effort_mappings"
        />
      </SettingsSection>
      <GroupRequestCompatibilityFields :id-prefix="idPrefix" v-model="form.routing_policy" />
    </template>
  </SettingsTabs>
</template>

<script setup lang="ts">
import LocalizedFieldsEditor from '@/components/common/LocalizedFieldsEditor.vue'
import { computed, nextTick, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Icon from '@/components/icons/Icon.vue'
import { defaultProviderBrandOptions } from '@/utils/providerBrand'
import SettingsTabs from '@/components/common/settings/SettingsTabs.vue'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import SettingToggleRow from '@/components/common/settings/SettingToggleRow.vue'
import GroupRoutingPolicyFields from './GroupRoutingPolicyFields.vue'
import GroupModelRoutingFields from './GroupModelRoutingFields.vue'
import GroupModelsListFields from './GroupModelsListFields.vue'
import GroupClientProtocolSelector from './GroupClientProtocolSelector.vue'
import ReasoningEffortPolicyFields from './ReasoningEffortPolicyFields.vue'
import GroupRequestCompatibilityFields from './GroupRequestCompatibilityFields.vue'
import type { ModelsListState } from '@/components/admin/group/groupsModelsList'
import type {
  GroupProviderSearchState,
  GroupModelRoutingRule,
  GroupRoutingProvider,
  GroupSettingsDraft,
  GroupSettingsOptions,
} from './groupSettingsTypes'

// GroupSettingsForm.vue composes the tabbed group editor and forwards draft updates to its
// page. Models, scheduling, protocol, and request sections share one group draft, while
// provider searches and model-list selections remain page-owned state. Child events carry rule
// identities back to that owner.
//
// A computed proxy translates field assignments into partial updates, allowing the page to
// retain the reactive draft object. Validation first asks the tab container to inspect fields,
// then checks reasoning mappings and reveals their error after Vue renders it. Exposed methods
// let page submission locate business validation failures in the appropriate tab.

// props supplies parent-owned editor values and display controls consumed by the fields below.
const props = defineProps<{
  mode: 'create' | 'edit' // selected editor mode
  // modelValue supplies the parent-owned value edited by these controls. Updates are emitted to
  // the parent for publication back through this prop.
  modelValue: GroupSettingsDraft
  options: GroupSettingsOptions // available form choices
  routingRules: GroupModelRoutingRule[] // page-owned routing rules
  modelsList: ModelsListState // editable model-list state
  modelsListLoading: boolean // candidate loading flag
  providerSearch: GroupProviderSearchState // page-owned search state
  getRuleKey: (rule: GroupModelRoutingRule) => string // stable rule-key resolver
}>()
// emit forwards typed edits and actions to the parent that owns the displayed draft.
const emit = defineEmits<{
  patch: [
    value: Partial<GroupSettingsDraft>, // replacement field value
  ] // partial group draft update
  configureScheduler: [] // open scheduler overrides
  addRule: [] // append routing rule
  removeRule: [
    rule: GroupModelRoutingRule, // owning routing rule
  ] // remove routing rule
  rulePattern: [
    rule: GroupModelRoutingRule, // owning routing rule
    value: string, // replacement field value
  ] // rule pattern update
  searchProviders: [
    rule: GroupModelRoutingRule, // owning routing rule
    keyword: string, // provider search text
  ] // rule and search keyword
  focusProviders: [
    rule: GroupModelRoutingRule, // owning routing rule
  ] // focused provider search
  selectProvider: [
    rule: GroupModelRoutingRule, // owning routing rule
    provider: GroupRoutingProvider, // selected provider record
  ] // rule and selected provider
  removeProvider: [
    rule: GroupModelRoutingRule, // owning routing rule
    providerId: number, // removed provider identifier
  ] // rule and removed provider
  modelsEnabled: [
    value: boolean, // replacement field value
  ] // custom-list enablement change
  selectModel: [
    id: string, // selected model identifier
    value: boolean, // replacement field value
  ] // model selection change
  selectAllModels: [] // select every model
  invertModels: [] // invert model selections
  moveModel: [
    from: number, // source list index
    to: number, // destination list index
  ] // source and destination indexes
}>()
// t resolves the current locale messages used by labels, validation, and action notifications.
const { t, locale } = useI18n()
// idPrefix namespaces field identifiers by create or edit mode so both form instances can
// coexist on the page.
const idPrefix = computed(() => `${props.mode}-group`)

// tabs organizes general, models, scheduling, protocol, and request settings under translated
// labels shared by every group.
const tabs = computed(() =>
  (['general', 'models', 'scheduling', 'protocol', 'request'] as const).map((key) => ({
    key,
    label: t(`admin.groups.tabs.${key}`),
  })),
)
// tabsRef and reasoningRef expose child validation and field-reveal methods used during
// whole-form submission from the page.
const tabsRef = ref<InstanceType<typeof SettingsTabs> | null>(null)
const reasoningRef = ref<InstanceType<typeof ReasoningEffortPolicyFields> | null>(null)

// form turns assignments into patch events, retaining the page-owned reactive draft and
// compatibility fields through child edits.
const form = computed(
  () =>
    new Proxy(props.modelValue, {
      // set forwards an assigned draft property as a partial update to the page.
      set(_target, key, value) {
        emit('patch', { [key]: value })
        return true
      },
    }),
)
// providerBrandOptions, statusOptions, schedulerOptions, and fastOptions supply the supported
// values shown by the general and scheduling controls.
const providerBrandOptions = defaultProviderBrandOptions
// statusOptions offers active and inactive values for the existing group editor, using the same
// translated activation labels as provider controls.
const statusOptions = computed(() =>
  ['active', 'inactive'].map((value) => ({
    value,
    label: t(`admin.providers.status.${value}`),
  })),
)
const schedulerOptions = computed(() =>
  ['basic', 'advanced'].map((value) => ({
    value,
    label: t(`admin.groups.scheduler.${value}`),
  })),
)
const fastOptions = computed(() => [
  {
    value: 'follow_request',
    label: t('admin.groups.openaiFast.followRequest'),
  },
  { value: 'force_priority', label: t('admin.groups.openaiFast.force') },
  {
    value: 'force_ultrafast',
    label: t('admin.groups.openaiFast.forceUltrafast'),
  },
  { value: 'force_off', label: t('admin.groups.openaiFast.forceOff') },
])
// advancedSummary counts configured scheduler keys so the closed override dialog still exposes
// whether group-specific values exist.
const advancedSummary = computed(() => {
  const count = Object.keys(props.modelValue.advanced_scheduler_overrides).length
  return count
    ? t('admin.groups.advancedSchedulerOverrides.overriddenCount', { count })
    : t('admin.groups.advancedSchedulerOverrides.allInherited')
})
// copyOptions disables already selected provider-copy sources, leaving their stored IDs
// available for removal in the form.
const copyOptions = computed(() =>
  props.options.copyProviders.map((option) => ({
    ...option,
    disabled: props.modelValue.copy_providers_from_group_ids.includes(Number(option.value)),
  })),
)
// copyGroupLabel resolves a provider-copy group identifier to its option label. Unavailable
// identifiers display their numeric ID so a saved selection remains recognizable when its
// option is missing.
function copyGroupLabel(id: number) {
  return props.options.copyProviders.find((option) => option.value === id)?.label ?? `#${id}`
}
// addCopyGroup appends a nonzero numeric group identifier to the draft selection. Already
// selected IDs are ignored, and assignment passes through the form proxy to emit a page patch.
function addCopyGroup(value: string | number | boolean | null) {
  const id = Number(value)
  if (id && !form.value.copy_providers_from_group_ids.includes(id)) {
    form.value.copy_providers_from_group_ids = [...form.value.copy_providers_from_group_ids, id]
  }
}

// validate checks tabbed fields and reasoning mappings before page submission. Failed reasoning
// validation waits for Vue to render the alert before revealing it; successful validation
// returns true to the caller.
async function validate() {
  if (tabsRef.value && !(await tabsRef.value.validate())) return false
  if (reasoningRef.value && !reasoningRef.value.validate()) {
    // Wait for reasoning validation to render its alert before asking the tab container to
    // reveal and focus the affected field.
    await nextTick()
    await tabsRef.value?.revealField('[data-group-field="reasoning"] [role="alert"]')
    return false
  }
  return true
}
// revealField delegates field discovery and tab selection to the settings container.
function revealField(selector: string) {
  return tabsRef.value?.revealField(selector)
}
// resetValidation clears visible reasoning errors through the mounted reasoning editor.
function resetValidation() {
  reasoningRef.value?.resetValidation()
}

defineExpose({ validate, revealField, resetValidation })
</script>
