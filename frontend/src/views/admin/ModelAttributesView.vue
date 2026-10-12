<template>
  <AppLayout>
    <div role="tablist" :aria-label="t('admin.modelAttributes.title')" class="mb-4 flex border-b border-gray-200 dark:border-dark-700">
      <button v-for="tab in ['configs', 'defaults'] as const" :key="tab" type="button" role="tab" :aria-selected="activeTab === tab" class="border-b-2 px-4 py-3 text-sm font-medium" :class="activeTab === tab ? 'border-primary-500 text-primary-700 dark:text-primary-300' : 'border-transparent text-gray-500 dark:text-dark-400'" @click="activeTab = tab">{{ t(`admin.modelAttributes.tabs.${tab}`) }}</button>
    </div>
    <TablePageLayout>
      <template #filters>
        <div class="space-y-2">
          <div class="flex flex-wrap items-start justify-between gap-2">
            <div class="flex min-w-0 flex-1 flex-nowrap items-center gap-2">
              <!-- 搜索框与价格管理共用尺寸和图标布局，提示随页签对应实际搜索对象。 -->
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

    <BaseDialog :show="showEditor" :title="t(form.id ? 'admin.modelAttributes.edit' : 'admin.modelAttributes.create')" width="extra-wide" @close="showEditor = false">
      <form id="attribute-form" class="space-y-4" @submit.prevent="save">
        <p v-if="formError" role="alert" class="text-sm text-red-600">{{ formError }}</p>
        <div class="grid gap-4 sm:grid-cols-2">
          <label><span class="input-label">{{ t('common.name') }}</span><input v-model="form.name" required maxlength="100" class="input" /></label>
          <div><label class="input-label">{{ t('common.status') }}</label><Select v-model="form.status" :options="editStatusOptions" /></div>
          <label class="sm:col-span-2"><span class="input-label">{{ t('admin.modelAttributes.configDescription') }}</span><textarea v-model="form.description" class="input" rows="2" /></label>
        </div>
        <div>
          <label class="input-label text-xs">
            {{ t('admin.modelAttributes.groups') }}
            <span v-if="form.group_ids.length" class="ml-1 font-normal text-gray-400">
              ({{ t('admin.pricing.form.selectedCount', { count: form.group_ids.length }) }})
            </span>
          </label>
          <!-- 分组选择沿用价格配置的徽章和选中底色，保留属性配置自身的关联关系。 -->
          <div class="max-h-40 overflow-auto rounded-control border border-gray-200 p-3 dark:border-dark-600">
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
        <RuleListEditor :items="form.rules" :title="t('admin.modelAttributes.rules')" :empty-text="t('admin.modelAttributes.emptyRules')" variant="card" @add="addRule()" @remove="form.rules.splice($event, 1)" @move="moveRule">
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
              <ModelAttributeRouteFields v-model="item.match" />
              <ModelAttributesFields v-model="item.attributes" :inherited="ruleDefaults[item.id || item.models[0] || '']" />
              <details v-if="ruleDefaults[item.id || item.models[0] || '']" class="text-sm">
                <summary class="cursor-pointer text-gray-500 dark:text-dark-400">{{ t('admin.modelAttributes.defaultReference') }}</summary>
                <ModelAttributesSummary class="mt-4" :attributes="ruleDefaults[item.id || item.models[0] || '']!" />
              </details>
            </div>
          </template>
        </RuleListEditor>
        <fieldset class="space-y-4 rounded-control border border-gray-200 p-4 dark:border-dark-600">
          <legend class="px-1 text-sm font-medium">{{ t('admin.modelAttributes.preview') }}</legend>
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.modelAttributes.previewHint') }}</p>
          <div class="grid gap-4 sm:grid-cols-2">
            <div><label class="input-label">{{ t('admin.modelAttributes.groups') }}</label><Select v-model="previewGroup" :options="previewGroups" /></div>
            <label><span class="input-label">{{ t('admin.modelAttributes.previewModel') }}</span><input v-model="previewModel" class="input" /></label>
          </div>
          <button type="button" class="btn btn-secondary" :disabled="previewLoading || !previewGroup || !previewModel.trim()" @click="preview">{{ t('admin.modelAttributes.preview') }}</button>
          <p v-if="previewError" role="alert" class="text-sm text-red-600">{{ previewError }}</p>
          <p v-else-if="previewLoaded && !previewRoutes.length" class="text-sm text-gray-500 dark:text-dark-400">{{ t('admin.modelAttributes.previewEmpty') }}</p>
          <div v-for="(route, index) in previewRoutes" :key="index" class="space-y-4 rounded-control border border-gray-200 p-4 dark:border-dark-600">
            <p class="break-words text-sm">{{ route.target.model }} · {{ route.target.platform }} · {{ route.target.provider_type }} · {{ route.target.provider_mode }} · {{ route.target.site }} · {{ route.target.catalog_provider }} · {{ route.target.client_protocol }} / {{ route.target.upstream_protocol }}</p>
            <ModelAttributesSummary :attributes="route.attributes" />
            <details class="text-sm">
              <summary class="cursor-pointer text-gray-500 dark:text-dark-400">{{ t('admin.modelAttributes.fieldSource') }}</summary>
              <dl class="mt-4 space-y-2"><div v-for="(source, field) in route.sources" :key="field" class="flex flex-wrap justify-between gap-2"><dt>{{ attributeFieldLabel(field) }}</dt><dd>{{ source === 'models.dev' ? 'models.dev' : t(`admin.modelAttributes.sources.${source}`) }}</dd></div></dl>
            </details>
          </div>
        </fieldset>
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
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
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
import { SEARCH_DEBOUNCE_MS } from '@/constants/ui'

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
const ruleDefaults = ref<Record<string, ModelAttributes>>({})
const previewGroup = ref(''), previewModel = ref(''), previewError = ref('')
const previewLoading = ref(false), previewLoaded = ref(false)
const previewRoutes = ref<ResolvedAttributes[]>([])
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
  ruleDefaults.value = {}
  previewGroup.value = config?.group_ids[0] ? String(config.group_ids[0]) : ''
  previewModel.value = config?.rules.flatMap(rule => rule.models).find(model => !model.includes('*')) ?? ''
  previewRoutes.value = []
  previewLoaded.value = false
  previewError.value = ''
  previewSequence++
  previewLoading.value = false
  formError.value = ''
  showEditor.value = true
  groupsLoading.value = true
  try { groups.value = await adminAPI.groups.getAll() }
  catch (cause) { formError.value = extractApiErrorMessage(cause, t('common.error')) }
  finally { groupsLoading.value = false }
}
function moveRule(from: number, to: number) {
  const item = form.value.rules.splice(from, 1)[0]
  if (item) form.value.rules.splice(to, 0, item)
}

// 默认查询作为参考展示，保存值来自管理员填写的覆盖字段。
async function onModelsUpdate(rule: AttributeRule, models: string[]) {
  const addedModels = models.filter(model => !rule.models.includes(model))
  rule.models = models
  const model = addedModels[0]
  if (!model || model.includes('*') || Object.keys(rule.attributes).length > 0) return

  const requestedModels = rule.models
  const attributes = rule.attributes
  try {
    const defaults = await modelAttributesAPI.getModelDefaultAttributes(model)
    // 慢请求不能覆盖后续输入、手动编辑或已经移除的规则。
    if (
      !showEditor.value || saving.value || !form.value.rules.includes(rule)
      || rule.models !== requestedModels || rule.attributes !== attributes
    ) return
    ruleDefaults.value[rule.id || model] = defaults
  } catch {
    // 目录查询失败时保留表单，用户仍可手动填写。
  }
}

async function preview() {
  if (!previewGroup.value || !previewModel.value.trim() || previewLoading.value) return
  const sequence = ++previewSequence
  const groupID = Number(previewGroup.value), model = previewModel.value.trim()
  const draft = JSON.stringify(form.value)
  previewLoading.value = true
  previewError.value = ''
  try {
    const result = await modelAttributesAPI.preview(groupID, model, JSON.parse(draft))
    if (sequence !== previewSequence || !showEditor.value || draft !== JSON.stringify(form.value) || groupID !== Number(previewGroup.value) || model !== previewModel.value.trim()) return
    previewRoutes.value = result.routes
    previewLoaded.value = true
  } catch (cause) {
    if (sequence === previewSequence && showEditor.value) previewError.value = extractApiErrorMessage(cause, t('common.error'))
  } finally {
    if (sequence === previewSequence) previewLoading.value = false
  }
}

function attributeFieldLabel(field: string) {
  const key = field.startsWith('media.') ? `admin.modelAttributes.mediaFields.${field.slice(6)}` : `admin.modelAttributes.fields.${field}`
  return te(key) ? t(key) : field
}

function addRule() {
  form.value.rules.push({ id: newContentID(), models: [], attributes: {} })
}

async function save() {
  if (saving.value) return
  formError.value = ''
  // 标签输入不依赖原生 required，提交前仍需阻止没有模型的规则。
  if (form.value.rules.some(rule => rule.models.length === 0)) {
    formError.value = t('admin.modelAttributes.modelsRequired')
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
watch(search, () => { clearTimeout(searchTimer); searchTimer = setTimeout(() => { page.value = 1; load() }, SEARCH_DEBOUNCE_MS) })
onMounted(load)
onUnmounted(() => { sequence++; showEditor.value = false; clearTimeout(searchTimer) })
</script>
