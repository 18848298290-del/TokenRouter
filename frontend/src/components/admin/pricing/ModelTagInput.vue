<template>
  <div>
    <!-- The container matches input backgrounds and borders; long model tags wrap while their remove controls remain visible. -->
    <TransitionGroup
      name="motion-list"
      @before-leave="prepareListLeave"
      @before-enter="restoreEnteringElement"
      tag="div"
      class="relative flex min-h-9 flex-wrap gap-2 rounded-control border border-primary-900/10 bg-white p-2 transition duration-fast focus-within:ring-2 focus-within:ring-black/10 dark:border-dark-600 dark:bg-dark-950 dark:focus-within:border-dark-400 dark:focus-within:ring-white/6"
    >
      <span
        v-for="(model, idx) in models"
        :key="model"
        class="inline-flex max-w-full items-center gap-2 rounded-compact px-2 py-1 text-sm"
        :class="getPlatformTagClass(props.platform || '')"
      >
        <span class="min-w-0 break-all">{{ model }}</span>
        <button
          type="button"
          @click="removeModel(idx)"
          :aria-label="`${t('common.delete')} ${model}`"
          class="shrink-0 rounded-compact opacity-70 hover:bg-black/10 hover:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:hover:bg-white/10"
        >
          <Icon name="x" size="xs" />
        </button>
      </span>
      <input
        key="model-input"
        v-model="inputValue"
        type="text"
        class="min-w-0 flex-1 basis-32 border-none bg-transparent text-sm outline-none placeholder:text-gray-400 dark:text-white"
        :aria-label="ariaLabel || placeholder || t('admin.pricing.form.modelInputHint')"
        :placeholder="models.length === 0 ? placeholder : ''"
        @keydown.enter.prevent="addModel"
        @keydown.tab.prevent="addModel"
        @keydown.delete="handleBackspace"
        @paste="handlePaste"
      />
    </TransitionGroup>
    <p class="mt-1 text-xs text-gray-400">
      {{
        t(
          'admin.pricing.form.modelInputHint',
          'Press Enter to add, supports paste for batch import.',
        )
      }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { prepareListLeave, restoreEnteringElement } from '@/utils/leavingElement'
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { getPlatformTagClass } from './types'

// ModelTagInput.vue edits ordered model patterns as removable tags for pricing and routing
// forms. Direct additions trim one value, while pasted lists split commas, semicolons, and
// newlines. Duplicate values are omitted and updates replace the parent's model array.

// t resolves the current locale messages used by labels, validation, and action notifications.
const { t } = useI18n()

// props supplies parent-owned editor values and display controls consumed by the fields below.
const props = defineProps<{
  models: string[] // ordered model patterns
  ariaLabel?: string // accessible input label
  placeholder?: string // empty input hint
  platform?: string // provider platform identifier
}>()

// emit forwards typed edits and actions to the parent that owns the displayed draft.
const emit = defineEmits<{
  'update:models': [
    models: string[], // replacement model identifiers
  ] // replacement parent-owned field value
}>()

// inputValue holds unfinished model text until an add or paste operation publishes the parsed
// tag list.
const inputValue = ref('')

// addModel appends the trimmed input when it is nonempty and absent from the list. Existing
// values are not duplicated, and a nonempty attempted addition clears the text input afterward.
function addModel() {
  const val = inputValue.value.trim()
  if (!val) return
  if (!props.models.includes(val)) {
    emit('update:models', [...props.models, val])
  }
  inputValue.value = ''
}

// removeModel emits a copied model list with the indexed tag removed.
function removeModel(idx: number) {
  const newModels = [...props.models]
  newModels.splice(idx, 1)
  emit('update:models', newModels)
}

// handleBackspace removes the final model tag when the input is empty. Nonempty draft text
// keeps normal text editing behavior, and an empty model list produces no update.
function handleBackspace() {
  if (inputValue.value === '' && props.models.length > 0) {
    removeModel(props.models.length - 1)
  }
}

// handlePaste replaces normal pasting with separated model-list insertion. Commas, newlines,
// and semicolons delimit trimmed values; unique values append in order and a handled nonempty
// paste clears the draft.
function handlePaste(e: ClipboardEvent) {
  e.preventDefault()
  const text = e.clipboardData?.getData('text') || ''
  const items = text
    .split(/[,\n;]+/)
    .map((s) => s.trim())
    .filter(Boolean)
  if (items.length === 0) return
  const unique = [...new Set([...props.models, ...items])]
  emit('update:models', unique)
  inputValue.value = ''
}
</script>
