<template>
  <SettingsSection data-testid="upstream-usage-config">
    <SettingToggleRow
      :id="`${uid}-enabled`"
      v-model="enabledModel"
      :label="t('admin.providers.upstreamUsage.title')"
      :hint="t('admin.providers.upstreamUsage.hint')"
      testid="upstream-usage-enabled"
    />

    <Collapse :open="enabledModel && !automaticAdapter" unmount-on-hide>
      <SettingsSubpanel>
        <div>
          <label class="input-label">{{ t('admin.providers.upstreamUsage.adapter') }}</label>
          <Select
            v-model="adapterModel"
            :options="adapterOptions"
            data-testid="upstream-usage-adapter"
          />
        </div>
        <div>
          <label class="input-label">{{ t('admin.providers.upstreamUsage.baseUrl') }}</label>
          <input
            v-model="baseUrlModel"
            type="text"
            class="input"
            :placeholder="
              adapterModel === 'derouter'
                ? 'https://cf-api.derouter.ai'
                : t('admin.providers.upstreamUsage.baseUrlPlaceholder')
            "
            data-testid="upstream-usage-base-url"
          />
          <p class="input-hint">
            {{
              t(
                adapterModel === 'derouter'
                  ? 'admin.providers.upstreamUsage.derouterBaseUrlHint'
                  : 'admin.providers.upstreamUsage.baseUrlHint',
              )
            }}
          </p>
        </div>
        <template v-if="adapterModel === 'new_api'">
          <div>
            <label class="input-label">{{
              t('admin.providers.upstreamUsage.walletAccessToken')
            }}</label>
            <input
              v-model="walletAccessTokenModel"
              type="password"
              class="input font-mono"
              autocomplete="new-password"
              :placeholder="t('admin.providers.upstreamUsage.walletAccessTokenPlaceholder')"
              data-testid="upstream-usage-wallet-access-token"
            />
            <p class="input-hint">{{ t('admin.providers.upstreamUsage.walletAccessTokenHint') }}</p>
          </div>
          <div>
            <label class="input-label">{{ t('admin.providers.upstreamUsage.walletUserId') }}</label>
            <input
              v-model="walletUserIdModel"
              type="text"
              inputmode="numeric"
              class="input"
              :placeholder="t('admin.providers.upstreamUsage.walletUserIdPlaceholder')"
              data-testid="upstream-usage-wallet-user-id"
            />
            <p class="input-hint">{{ t('admin.providers.upstreamUsage.walletUserIdHint') }}</p>
          </div>
        </template>
      </SettingsSubpanel>
    </Collapse>
  </SettingsSection>
</template>

<script setup lang="ts">
import Collapse from '@/components/common/Collapse.vue'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import SettingsSubpanel from '@/components/common/settings/SettingsSubpanel.vue'
import SettingToggleRow from '@/components/common/settings/SettingToggleRow.vue'

import { computed, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import type { UpstreamUsageAdapter } from '@/api/admin/providers.types'

// UpstreamUsageConfigEditor.vue edits a provider usage-query adapter, management address,
// and optional wallet credentials. Each input emits its own value to the parent form. The
// automatic adapter flag changes the visible controls, and New API exposes wallet
// authentication independently from inference credentials.

// props exposes caller-owned inputs used by the template and derived state in this
// component.
const props = withDefaults(
  defineProps<{
    // enabled, adapter, and baseUrl describe the management query. walletAccessToken and
    // walletUserId belong to New API wallet authentication; parent hydration and patch
    // construction keep blank token drafts and clear blank nonsecret user identifiers.
    enabled: boolean // Parent-owned feature enable draft.
    adapter: UpstreamUsageAdapter // Usage-query adapter wire identifier.
    baseUrl: string // Independent management query address.
    // New API replacement token; EditProviderModal keeps the server-stored secret when
    // this draft is empty.
    walletAccessToken?: string
    walletUserId?: string // New API wallet user identifier.
    // When true, hides manual adapter selection while retaining the caller-supplied
    // adapter draft value.
    automaticAdapter?: boolean
  }>(),
  {
    enabled: true,
    adapter: 'sub2api',
    baseUrl: '',
    walletAccessToken: '',
    walletUserId: '',
    automaticAdapter: false,
  },
)

// emit publishes typed draft changes and actions to the parent that owns this component.
const emit = defineEmits<{
  'update:enabled': [value: boolean] // Replacement enabled draft value.
  'update:adapter': [value: UpstreamUsageAdapter] // Replacement adapter draft value.
  'update:baseUrl': [value: string] // Replacement base url draft value.
  'update:walletAccessToken': [value: string] // Replacement wallet access token draft value.
  'update:walletUserId': [value: string] // Replacement wallet user id draft value.
}>()

// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()
// uid supplies one stable instance ID so labels target their own input when several
// editors are mounted.
const uid = useId()

// adapterOptions lists manually selectable usage formats with localized names while their
// wire values remain stable for parent payload construction.
const adapterOptions = computed<SelectOption[]>(() => [
  { value: 'sub2api', label: t('admin.providers.upstreamUsage.adapters.sub2api') },
  { value: 'new_api', label: t('admin.providers.upstreamUsage.adapters.newApi') },
  { value: 'zivv', label: t('admin.providers.upstreamUsage.adapters.zivv') },
  { value: 'derouter', label: t('admin.providers.upstreamUsage.adapters.derouter') },
])

// enabledModel forwards the query switch to the parent and reflects its current value in
// the shared toggle row.
const enabledModel = computed({
  get: () => props.enabled,
  set: (value: boolean) => emit('update:enabled', value),
})

// adapterModel accepts registered manual adapter values and ignores cleared or unknown
// selector values before emitting a parent update.
const adapterModel = computed({
  get: () => props.adapter,
  set: (value: string | number | boolean | null) => {
    if (value === 'sub2api' || value === 'new_api' || value === 'zivv' || value === 'derouter')
      emit('update:adapter', value)
  },
})

// baseUrlModel forwards the independent management address draft to the parent without
// normalizing its text inside this editor.
const baseUrlModel = computed({
  get: () => props.baseUrl,
  set: (value: string) => emit('update:baseUrl', value),
})

// walletAccessTokenModel forwards replacement token text. EditProviderModal initializes
// this draft empty for redacted credentials and writes a replacement only when the
// administrator enters nonblank text.
const walletAccessTokenModel = computed({
  get: () => props.walletAccessToken,
  set: (value: string) => emit('update:walletAccessToken', value),
})

// walletUserIdModel forwards the optional wallet user ID. EditProviderModal removes its
// nonsecret credential key when this draft is empty and writes the trimmed value
// otherwise.
const walletUserIdModel = computed({
  get: () => props.walletUserId,
  set: (value: string) => emit('update:walletUserId', value),
})
</script>
