<template>
  <div ref="dropdownRef" class="relative" data-tour="keys-scope-switch">
    <button
      type="button"
      class="btn btn-secondary md:w-auto md:min-h-0 md:px-3 md:py-1.5 btn-icon"
      :title="t('team.scopeSwitch')"
      :aria-expanded="open"
      aria-haspopup="menu"
      data-test="scope-dropdown-trigger"
      @click="open = !open"
    >
      <Icon :name="scope === 'team' ? 'users' : 'user'" size="sm" class="md:mr-1.5" />
      <span class="hidden md:inline">{{ currentLabel }}</span>
      <Icon name="chevronDown" size="xs" class="ml-1 hidden md:inline" :animate-on-hover="false" />
    </button>

    <MotionTransition name="dropdown-fade">
      <div
        v-if="open"
        :inert="!open || undefined"
        class="dropdown right-0 top-full z-50 mt-2 w-48 p-2"
        role="menu"
        data-test="scope-dropdown-menu"
      >
        <button
          v-for="option in options"
          :key="option.value"
          type="button"
          role="menuitemradio"
          :aria-checked="scope === option.value"
          class="dropdown-item-sm rounded-control"
          :data-test="`scope-option-${option.value}`"
          @click="setScope(option.value)"
        >
          <Icon :name="option.icon" size="sm" class="text-gray-400 dark:text-gray-500" />
          <span class="flex-1">{{ option.label }}</span>
          <Icon
            v-if="scope === option.value"
            name="check"
            size="sm"
            class="text-primary-500"
            :stroke-width="2"
            :animate-on-hover="false"
          />
        </button>
      </div>
    </MotionTransition>
  </div>
</template>

<script setup lang="ts">
import MotionTransition from '@/components/common/MotionTransition.vue'
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import Icon from '@/components/icons/Icon.vue'

// ScopeDropdown.vue switches key lists between personal and team ownership. The parent
// controls the selected value, while this dropdown preserves the scope in the route query
// and notifies the list owner after that navigation completes.

// DataScope selects personal or team ownership for the consuming key list.
export type DataScope = 'personal' | 'team'

// modelValue is the parent-owned list scope; the dropdown emits its selected value before
// persisting the URL query.
const props = defineProps<{ modelValue: DataScope /* Parent-owned list scope. */ }>()
// The model update changes visible selection immediately; change follows successful query
// replacement so the page reloads the chosen ownership scope.
const emit = defineEmits<{
  (event: 'update:modelValue', value: DataScope): void // Selected ownership scope.
  (event: 'change', value: DataScope): void // URL-persisted scope selection.
}>()

// Route dependencies retain unrelated query parameters, while local open state and the root
// element control dropdown dismissal and selected-label rendering.
const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const open = ref(false)
const dropdownRef = ref<HTMLElement | null>(null)
const scope = computed(() => props.modelValue)
const options = computed(() => [
  { value: 'personal' as const, label: t('team.personalKeys'), icon: 'user' as const },
  { value: 'team' as const, label: t('team.teamKeys'), icon: 'users' as const },
])
const currentLabel = computed(
  () => options.value.find((option) => option.value === scope.value)?.label || '',
)

// setScope closes the menu and publishes a different selected scope before replacing its URL
// query. The change event follows navigation completion so the list owner can reload with
// the persisted scope.
const setScope = async (value: DataScope) => {
  open.value = false
  if (value === scope.value) return
  emit('update:modelValue', value)
  await router.replace({ query: { ...route.query, scope: value } })
  emit('change', value)
}

// closeOnOutsideClick closes the dropdown when a captured document click targets outside its
// root. Mount and unmount own the same capture listener so nested click handlers do not
// prevent dismissal.
const closeOnOutsideClick = (event: MouseEvent) => {
  if (!dropdownRef.value?.contains(event.target as Node)) open.value = false
}

onMounted(() => document.addEventListener('click', closeOnOutsideClick, true))
onUnmounted(() => document.removeEventListener('click', closeOnOutsideClick, true))
</script>
