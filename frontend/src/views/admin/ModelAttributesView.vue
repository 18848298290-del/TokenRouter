<template>
  <AppLayout>
    <div role="tablist" :aria-label="t('admin.modelAttributes.title')" class="mb-4 flex border-b border-gray-200 dark:border-dark-700">
      <button v-for="tab in ['configs', 'defaults'] as const" :key="tab" type="button" role="tab" :aria-selected="activeTab === tab" class="border-b-2 px-4 py-3 text-sm font-medium" :class="activeTab === tab ? 'border-primary-500 text-primary-700 dark:text-primary-300' : 'border-transparent text-gray-500 dark:text-dark-400'" @click="activeTab = tab">{{ t(`admin.modelAttributes.tabs.${tab}`) }}</button>
    </div>
    <TablePageLayout>
      <template #filters>
        <div class="space-y-2">
          <div class="flex flex-wrap items-start justify-between gap-2">
            <!-- Give mobile filters a full row so action buttons cannot collapse the search input beneath its icon. -->
            <div class="flex w-full min-w-0 flex-none items-center gap-2 sm:w-auto">
              <div class="input-icon-wrap min-w-0 flex-1 sm:w-64 sm:flex-none">
                <Icon name="search" size="md" class="input-icon text-gray-400 dark:text-gray-500" />
                <input
                  v-model="search"
                  type="text"
                  class="input input-has-icon"
                  :placeholder="searchPlaceholder"
                  :aria-label="searchPlaceholder"
                />
              </div>
              <Select v-if="activeTab === 'configs'" v-model="status" :options="statusOptions" class="w-32 shrink-0" />
              <FilterDropdown v-else :active-count="activeFilterCount" @reset="provider = ''; capability = ''">
                <FilterField :label="t('admin.modelAttributes.columns.provider')">
                  <Select v-model="provider" :options="providerOptions" :aria-label="t('admin.modelAttributes.allProviders')" />
                </FilterField>
                <FilterField :label="t('admin.modelAttributes.columns.capability')">
                  <Select v-model="capability" :options="capabilityOptions" :aria-label="t('admin.modelAttributes.allCapabilities')" />
                </FilterField>
              </FilterDropdown>
            </div>
            <div class="ml-auto flex shrink-0 flex-wrap items-center justify-end gap-2">
              <button class="btn btn-secondary btn-icon" :disabled="loading || updating" :aria-label="t('common.refresh')" @click="load"><Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" /></button>
              <button v-if="activeTab === 'configs'" class="btn btn-primary" @click="edit()"><Icon name="plus" size="sm" class="mr-2" />{{ t('admin.modelAttributes.create') }}</button>
              <button v-else class="btn btn-primary" :disabled="updating" @click="updateCatalog">{{ t(updating ? 'admin.pricing.defaults.updating' : 'admin.pricing.defaults.update') }}</button>
            </div>
          </div>
          <ModelCatalogInfo v-if="activeTab === 'defaults'" :version="version" :updated-at="updatedAt" />
          <p v-if="error || (activeTab === 'defaults' && catalogError)" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error || catalogError }}</p>
        </div>
      </template>
      <template #table>
        <DataTable v-if="activeTab === 'configs'" :columns="configColumns" :data="configs" :loading="loading" column-order-storage-key="model-attribute-config-columns">
          <template #cell-status="{ row }"><Toggle :model-value="row.status === 'active'" @update:model-value="toggleStatus(row)" /></template>
          <template #cell-groups="{ row }">{{ row.group_ids.length }}</template>
          <template #cell-rules="{ row }">{{ row.rules.length }}</template>
          <template #cell-actions="{ row }">
            <!-- 行内操作和价格管理一致：图标在上、文字在下，删除悬停时变红。 -->
            <div class="flex items-center gap-1">
              <button
                type="button"
                class="flex flex-col items-center gap-0.5 rounded-control p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-primary-600 dark:hover:bg-dark-700 dark:hover:text-primary-400"
                :aria-label="t('common.edit')"
                @click="edit(row)"
              >
                <Icon name="edit" size="sm" />
                <span class="text-xs">{{ t('common.edit') }}</span>
              </button>
              <button
                type="button"
                class="flex flex-col items-center gap-0.5 rounded-control p-1.5 text-gray-500 transition-colors hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20 dark:hover:text-red-400"
                :aria-label="t('common.delete')"
                @click="deleting = row"
              >
                <Icon name="trash" size="sm" />
                <span class="text-xs">{{ t('common.delete') }}</span>
              </button>
            </div>
          </template>
        </DataTable>
        <DataTable v-else :columns="defaultColumns" :data="defaults" :loading="loading" column-order-storage-key="model-default-attribute-columns">
          <template #cell-name="{ row }">{{ row.attributes.display_name ?? t('admin.modelAttributes.unknown') }}</template>
          <template #cell-context="{ row }">{{ row.attributes.context?.toLocaleString() ?? t('admin.modelAttributes.unknown') }}</template>
          <template #cell-output="{ row }">{{ row.attributes.output_limit?.toLocaleString() ?? t('admin.modelAttributes.unknown') }}</template>
          <template #cell-actions="{ row }">
            <button
              type="button"
              class="flex flex-col items-center gap-0.5 rounded-control p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-primary-600 dark:hover:bg-dark-700 dark:hover:text-primary-400"
              :title="t('admin.modelAttributes.details')"
              :aria-label="t('admin.modelAttributes.details')"
              @click="detail = row"
            >
              <Icon name="eye" size="sm" />
              <span class="text-xs">{{ t('admin.modelAttributes.details') }}</span>
            </button>
          </template>
        </DataTable>
      </template>
      <template #pagination>
        <Pagination v-if="total" :page="page" :total="total" :page-size="pageSize" @update:page="page = $event; load()" @update:page-size="pageSize = $event; page = 1; load()" />
      </template>
    </TablePageLayout>

    <BaseDialog :show="showEditor" :title="t(form.id ? 'admin.modelAttributes.edit' : 'admin.modelAttributes.create')" width="extra-wide" :body-scroll="false" @close="showEditor = false">
      <form id="attribute-form" class="flex min-h-0 flex-1 flex-col" novalidate @submit.prevent="save">
        <p v-if="formError" role="alert" class="mb-4 text-sm text-red-600 dark:text-red-400">{{ formError }}</p>
        <SettingsTabs ref="tabsRef" id-prefix="model-attribute-editor" :label="t(form.id ? 'admin.modelAttributes.edit' : 'admin.modelAttributes.create')" :tabs="editorTabs">
          <template #basic>
            <div class="space-y-4">
              <div class="grid gap-4 sm:grid-cols-2">
                <label><span class="input-label">{{ t('common.name') }}</span><input v-model="form.name" required maxlength="100" class="input" /></label>
                <div><label class="input-label">{{ t('common.status') }}</label><Select v-model="form.status" :aria-label="t('common.status')" :options="editStatusOptions" /></div>
                <label class="sm:col-span-2"><span class="input-label">{{ t('admin.modelAttributes.configDescription') }}</span><textarea v-model="form.description" class="input" rows="2" /></label>
              </div>
              <div>
                <label class="input-label">
                  {{ t('admin.modelAttributes.groups') }}
                  <span v-if="form.group_ids.length" class="ml-1 font-normal text-gray-400">
                    ({{ t('admin.pricing.form.selectedCount', { count: form.group_ids.length }) }})
                  </span>
                </label>
                <!-- 分组选择和价格配置使用同样的徽章和选中底色。 -->
                <div class="max-h-menu overflow-auto rounded-control border border-gray-200 p-3 dark:border-dark-600">
                  <p v-if="groupsLoading" class="py-2 text-center text-xs text-gray-500">{{ t('common.loading') }}</p>
                  <p v-else-if="!groups.length" class="py-2 text-center text-xs text-gray-500">{{ t('admin.pricing.form.noGroupsAvailable') }}</p>
                  <div v-else class="flex flex-wrap gap-2">
                    <label
                      v-for="group in groups"
                      :key="group.id"
                      class="inline-flex max-w-full cursor-pointer items-center gap-2 rounded-control p-1.5 transition-colors hover:bg-gray-50 dark:hover:bg-dark-700"
                      :class="form.group_ids.includes(group.id) ? 'bg-primary-50 dark:bg-primary-500/8 dark:text-primary-500' : ''"
                    >
                      <input v-model="form.group_ids" type="checkbox" :value="group.id" class="h-4 w-4 shrink-0 rounded-compact border-gray-300 text-primary-600 focus:ring-primary-500" />
                      <GroupBadge :name="group.name" :display-brand="group.display_brand" :rate-multiplier="group.rate_multiplier" class="min-w-0" />
                    </label>
                  </div>
                </div>
              </div>
            </div>
          </template>
          <template #rules>
            <RuleListEditor
              :items="form.rules"
              :title="t('admin.modelAttributes.rules')"
              :empty-text="t('admin.modelAttributes.emptyRules')"
              :item-label="(index) => t('common.ruleIndex', { index: index + 1 })"
              variant="card"
              @add="addRule()"
              @remove="form.rules.splice($event, 1)"
              @move="moveRule"
            >
              <template #row="{ item }">
                <div class="space-y-4">
                  <div>
                    <label class="input-label">{{ t('admin.modelAttributes.models') }}</label>
                    <ModelTagInput
                      :models="item.models"
                      :aria-label="t('admin.modelAttributes.models')"
                      :placeholder="t('admin.modelAttributes.modelHint')"
                      @update:models="onModelsUpdate(item, $event)"
                    />
                  </div>
                  <ModelAttributesFields v-model="item.attributes" :inherited="ruleDefaults.get(item)?.attributes" />
                  <ModelAttributeRouteFields v-model="item.match" />
                  <ModelAttributeDisclosure v-if="ruleDefaults.get(item)?.attributes" :title="t('admin.modelAttributes.defaultReference')" :meta="ruleDefaults.get(item)?.model">
                    <ModelAttributesSummary :attributes="ruleDefaults.get(item)!.attributes!" />
                  </ModelAttributeDisclosure>
                </div>
              </template>
            </RuleListEditor>
          </template>
          <template #preview>
            <div class="space-y-4">
              <p class="input-hint">{{ t('admin.modelAttributes.previewHint') }}</p>
              <div class="grid items-end gap-4 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]">
                <div><label class="input-label">{{ t('admin.modelAttributes.groups') }}</label><Select v-model="previewGroup" :aria-label="t('admin.modelAttributes.groups')" :options="previewGroups" /></div>
                <label><span class="input-label">{{ t('admin.modelAttributes.previewModel') }}</span><input v-model="previewModel" class="input" /></label>
                <button type="button" class="btn btn-secondary" :disabled="previewLoading || !previewGroup || !previewModel.trim()" @click="preview">
                  <Icon name="eye" size="sm" class="mr-2" />{{ t('admin.modelAttributes.preview') }}
                </button>
              </div>
              <p v-if="previewError" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ previewError }}</p>
              <p v-else-if="previewRoutes?.length === 0" class="rounded-control border border-dashed border-gray-200 px-3 py-4 text-center text-sm text-gray-500 dark:border-dark-600 dark:text-dark-400">{{ t('admin.modelAttributes.previewEmpty') }}</p>
              <div v-for="(route, index) in previewRoutes ?? []" :key="index" class="space-y-4 rounded-surface border border-gray-200 p-4 dark:border-dark-600">
                <div class="flex flex-wrap items-center gap-2">
                  <span class="break-all font-mono text-sm font-medium text-gray-900 dark:text-dark-50">{{ route.target.model }}</span>
                  <span v-for="tag in routeTags(route)" :key="tag" class="rounded-compact bg-gray-100 px-2 py-0.5 text-xs text-gray-600 dark:bg-dark-700 dark:text-dark-200">{{ tag }}</span>
                </div>
                <ModelAttributesSummary :attributes="route.attributes" />
                <ModelAttributeDisclosure :title="t('admin.modelAttributes.fieldSource')">
                  <dl class="space-y-2 text-sm">
                    <div v-for="(source, field) in route.sources" :key="field" class="flex flex-wrap justify-between gap-2">
                      <dt class="text-gray-500 dark:text-dark-400">{{ attributeFieldLabel(field) }}</dt>
                      <dd class="text-gray-900 dark:text-dark-50">{{ sourceLabel(source) }}</dd>
                    </div>
                  </dl>
                </ModelAttributeDisclosure>
              </div>
            </div>
          </template>
        </SettingsTabs>
      </form>
      <template #footer><div class="flex justify-end gap-2"><button class="btn btn-secondary" @click="showEditor = false">{{ t('common.cancel') }}</button><button form="attribute-form" type="submit" class="btn btn-primary" :disabled="saving || groupsLoading">{{ t('common.save') }}</button></div></template>
    </BaseDialog>

    <BaseDialog :show="!!detail" :title="detail?.model ?? ''" @close="detail = null">
      <div v-if="detail" class="space-y-4">
        <p class="text-sm text-gray-500">{{ detail.source }} · {{ detail.provider }}</p>
        <p v-if="detail.canonical_model_id" class="break-all text-sm">{{ detail.canonical_model_id }}</p>
        <ModelAttributesSummary :attributes="detail.attributes" />
      </div>
    </BaseDialog>
    <ConfirmDialog :show="!!deleting" :title="t('common.delete')" :message="t('admin.modelAttributes.deleteConfirm', { name: deleting?.name })" :danger="true" @confirm="remove" @cancel="deleting = null" />
  </AppLayout>
</template>

<script setup lang="ts">
import { newContentID } from '@/i18n/content'
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import RuleListEditor from '@/components/common/RuleListEditor.vue'
import Select from '@/components/common/Select.vue'
import FilterDropdown from '@/components/common/FilterDropdown.vue'
import FilterField from '@/components/common/FilterField.vue'
import GroupBadge from '@/components/common/GroupBadge.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import SettingsTabs from '@/components/common/settings/SettingsTabs.vue'
import ModelAttributeDisclosure from '@/components/admin/ModelAttributeDisclosure.vue'
import ModelAttributesFields from '@/components/admin/ModelAttributesFields.vue'
import ModelAttributeRouteFields from '@/components/admin/ModelAttributeRouteFields.vue'
import ModelCatalogInfo from '@/components/admin/ModelCatalogInfo.vue'
import ModelTagInput from '@/components/admin/pricing/ModelTagInput.vue'
import ModelAttributesSummary from '@/components/common/ModelAttributesSummary.vue'
import { modelAttributesAPI, type AttributeConfig, type AttributeRule, type DefaultAttributes, type ResolvedAttributes } from '@/api/admin/modelAttributes'
import { adminAPI } from '@/api/admin'
import type { AdminGroup } from '@/types'
import { attributeCapabilities, type ModelAttributes } from '@/types/modelAttributes'
import { extractApiErrorMessage } from '@/utils/apiError'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'
import { SEARCH_DEBOUNCE_MS } from '@/constants/ui'

// Model attribute configurations apply ordered rules to selected groups, while
// the defaults tab exposes catalog attributes for model lookup. The editor keeps
// a separate draft for validation and route previews before saving the configuration.
//
// List requests share a sequence counter so a slow response cannot replace the
// results of a newer tab or filter selection. Preview requests have their own
// counter and are invalidated when the draft, group, or model changes. Reference
// attributes are stored per rule and accepted only while the editor is open and
// the selected reference model still matches the request.

const { t, te } = useI18n()
const activeTab = ref<'configs' | 'defaults'>('configs')
const searchPlaceholder = computed(() => t(activeTab.value === 'configs'
  ? 'admin.modelAttributes.searchConfigs'
  : 'admin.modelAttributes.searchModels'))
const search = ref(''), status = ref(''), provider = ref(''), capability = ref('')
const activeFilterCount = computed(() => Number(!!provider.value) + Number(!!capability.value))
const page = ref(1), pageSize = ref(20), total = ref(0)
const loading = ref(false), updating = ref(false), saving = ref(false), showEditor = ref(false), groupsLoading = ref(false)
const error = ref(''), formError = ref(''), catalogError = ref(''), version = ref(''), updatedAt = ref('')
const configs = ref<AttributeConfig[]>([]), defaults = ref<DefaultAttributes[]>([]), providers = ref<string[]>([])
const groups = ref<AdminGroup[]>([])
const detail = ref<DefaultAttributes | null>(null), deleting = ref<AttributeConfig | null>(null)
const blank = (): AttributeConfig => ({ name: '', description: '', status: 'active', group_ids: [], rules: [] })
const form = ref<AttributeConfig>(blank())
const tabsRef = ref<InstanceType<typeof SettingsTabs> | null>(null)
const editorTabs = computed(() => (['basic', 'rules', 'preview'] as const).map(key => ({ key, label: t(`admin.modelAttributes.editorTabs.${key}`) })))
// 每条规则按第一个非通配模型查询目录默认值，只用于占位提示和参考展示。
const ruleDefaults = reactive(new WeakMap<AttributeRule, { model: string; attributes?: ModelAttributes }>())
const previewGroup = ref(''), previewModel = ref(''), previewError = ref(''), previewLoading = ref(false)
// null 表示还没有预览结果，空数组表示没有匹配的路线。
const previewRoutes = ref<ResolvedAttributes[] | null>(null)
const previewGroups = computed(() => groups.value.map(group => ({ value: String(group.id), label: group.name })))
let previewSequence = 0
const editStatusOptions = computed(() => [{ value: 'active', label: t('common.active') }, { value: 'disabled', label: t('common.disabled') }])
const statusOptions = computed(() => [{ value: '', label: t('admin.modelAttributes.allStatuses') }, ...editStatusOptions.value])
const providerOptions = computed(() => [{ value: '', label: t('admin.modelAttributes.allProviders') }, ...providers.value.map(value => ({ value, label: value }))])
const capabilityOptions = computed(() => [{ value: '', label: t('admin.modelAttributes.allCapabilities') }, ...attributeCapabilities.map(value => ({ value, label: t(`admin.modelAttributes.fields.${value}`) }))])
const configColumns = computed(() => ['name', 'status', 'groups', 'rules', 'actions'].map(key => ({ key, label: t(`admin.modelAttributes.columns.${key}`) })))
const defaultColumns = computed(() => ['model', 'name', 'provider', 'context', 'output', 'actions'].map(key => ({ key, label: t(`admin.modelAttributes.columns.${key}`) })))
let sequence = 0
let searchTimer: ReturnType<typeof setTimeout> | undefined

// 请求编号阻止慢查询覆盖用户切换后的页签和筛选结果。
async function load() {
  const current = ++sequence
  loading.value = true
  error.value = ''
  try {
    if (activeTab.value === 'configs') {
      const result = await modelAttributesAPI.list({ page: page.value, page_size: pageSize.value, search: search.value, status: status.value })
      if (current !== sequence) return
      configs.value = result.items
      total.value = result.total
    } else {
      const result = await modelAttributesAPI.defaults({ page: page.value, page_size: pageSize.value, search: search.value, provider: provider.value, capability: capability.value })
      if (current !== sequence) return
      defaults.value = result.items
      total.value = result.total
      providers.value = result.providers
      version.value = result.version
      updatedAt.value = result.last_updated
      catalogError.value = result.last_error ?? ''
    }
  } catch (cause) {
    if (current === sequence) error.value = extractApiErrorMessage(cause, t('common.error'))
  } finally {
    if (current === sequence) loading.value = false
  }
}
async function edit(config?: AttributeConfig) {
  form.value = config ? JSON.parse(JSON.stringify(config)) : blank()
  previewGroup.value = config?.group_ids[0] ? String(config.group_ids[0]) : ''
  previewModel.value = config?.rules.flatMap(rule => rule.models).find(model => !model.includes('*')) ?? ''
  formError.value = ''
  showEditor.value = true
  void tabsRef.value?.selectTab('basic')
  for (const rule of form.value.rules) void loadRuleDefaults(rule)
  groupsLoading.value = true
  try { groups.value = await adminAPI.groups.getAll() }
  catch (cause) { formError.value = extractApiErrorMessage(cause, t('common.error')) }
  finally { groupsLoading.value = false }
}
function moveRule(from: number, to: number) {
  const item = form.value.rules.splice(from, 1)[0]
  if (item) form.value.rules.splice(to, 0, item)
}

function onModelsUpdate(rule: AttributeRule, models: string[]) {
  rule.models = models
  void loadRuleDefaults(rule)
}

async function loadRuleDefaults(rule: AttributeRule) {
  const model = rule.models.find(item => !item.includes('*'))
  if (model === ruleDefaults.get(rule)?.model) return
  if (!model) {
    ruleDefaults.delete(rule)
    return
  }
  ruleDefaults.set(rule, { model })
  try {
    const attributes = await modelAttributesAPI.getModelDefaultAttributes(model)
    // 等待期间关闭弹窗或换了参考模型时，丢弃这次结果。
    if (!showEditor.value || ruleDefaults.get(rule)?.model !== model) return
    ruleDefaults.set(rule, { model, attributes })
  } catch {
    // 查询失败时没有参考值，字段仍可手动填写。
  }
}

// 草稿、分组或模型变化后，旧结果和进行中的请求都作废。
function resetPreview() {
  previewSequence++
  previewRoutes.value = null
  previewError.value = ''
  previewLoading.value = false
}

async function preview() {
  if (!previewGroup.value || !previewModel.value.trim() || previewLoading.value) return
  const sequence = ++previewSequence
  previewLoading.value = true
  previewError.value = ''
  try {
    const result = await modelAttributesAPI.preview(Number(previewGroup.value), previewModel.value.trim(), form.value)
    if (sequence === previewSequence) previewRoutes.value = result.routes
  } catch (cause) {
    if (sequence === previewSequence) previewError.value = extractApiErrorMessage(cause, t('common.error'))
  } finally {
    if (sequence === previewSequence) previewLoading.value = false
  }
}

function routeTags(route: ResolvedAttributes) {
  const { platform, provider_type, provider_mode, site, catalog_provider, client_protocol, upstream_protocol } = route.target
  const platformLabel = CONCRETE_PLATFORM_OPTIONS.find(option => option.value === platform)?.label ?? platform
  const protocols = [client_protocol, upstream_protocol].filter(Boolean).join(' → ')
  return [platformLabel, provider_type, provider_mode, site, catalog_provider, protocols].filter((tag): tag is string => !!tag)
}

// 来源字段名和表单字段共用文案，没有文案的字段直接显示字段名。
function attributeFieldLabel(field: string) {
  const key = field.startsWith('media.') ? `admin.modelAttributes.mediaFields.${field.slice(6)}` : `admin.modelAttributes.fields.${field}`
  return te(key) ? t(key) : field
}

function sourceLabel(source: string) {
  const key = `admin.modelAttributes.sources.${source}`
  return te(key) ? t(key) : source
}

function addRule() {
  form.value.rules.push({ id: newContentID(), models: [], attributes: {} })
}

async function save() {
  if (saving.value) return
  formError.value = ''
  if (tabsRef.value && !(await tabsRef.value.validate())) return
  // 标签输入不参与原生校验，没有模型的规则在这里拦截。
  if (form.value.rules.some(rule => rule.models.length === 0)) {
    formError.value = t('admin.modelAttributes.modelsRequired')
    void tabsRef.value?.selectTab('rules')
    return
  }
  saving.value = true
  try { await modelAttributesAPI.save(form.value); showEditor.value = false; await load() }
  catch (cause) { formError.value = extractApiErrorMessage(cause, t('common.error')) }
  finally { saving.value = false }
}
async function toggleStatus(config: AttributeConfig) {
  try { await modelAttributesAPI.save({ ...config, status: config.status === 'active' ? 'disabled' : 'active' }); await load() }
  catch (cause) { error.value = extractApiErrorMessage(cause, t('common.error')) }
}
async function remove() {
  const id = deleting.value?.id
  deleting.value = null
  if (!id) return
  try { await modelAttributesAPI.remove(id); await load() }
  catch (cause) { error.value = extractApiErrorMessage(cause, t('common.error')) }
}
async function updateCatalog() {
  if (updating.value) return
  updating.value = true
  error.value = ''
  try { await modelAttributesAPI.update(); await load() }
  catch (cause) { error.value = extractApiErrorMessage(cause, t('common.error')) }
  finally { updating.value = false }
}
watch([activeTab, status, provider, capability], () => { page.value = 1; load() })
watch([form, previewGroup, previewModel], resetPreview, { deep: true })
watch(search, () => { clearTimeout(searchTimer); searchTimer = setTimeout(() => { page.value = 1; load() }, SEARCH_DEBOUNCE_MS) })
onMounted(load)
onUnmounted(() => { sequence++; showEditor.value = false; clearTimeout(searchTimer) })
</script>
