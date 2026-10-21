<template>
  <div v-if="attributes.length > 0" class="space-y-4">
    <div v-for="attr in attributes" :key="attr.id">
      <label class="input-label">
        {{ attr.name }}
        <span v-if="attr.required" class="text-red-500">*</span>
      </label>

      <!-- Text Input -->
      <input
        v-if="attr.type === 'text' || attr.type === 'email' || attr.type === 'url'"
        v-model="localValues[attr.id]"
        :type="attr.type === 'text' ? 'text' : attr.type"
        :required="attr.required"
        :placeholder="attr.placeholder"
        class="input"
        @input="emitChange"
      />

      <!-- Number Input -->
      <input
        v-else-if="attr.type === 'number'"
        v-model.number="localValues[attr.id]"
        type="number"
        :required="attr.required"
        :placeholder="attr.placeholder"
        :min="attr.validation?.min"
        :max="attr.validation?.max"
        class="input"
        @input="emitChange"
      />

      <!-- Date Input -->
      <input
        v-else-if="attr.type === 'date'"
        v-model="localValues[attr.id]"
        type="date"
        :required="attr.required"
        class="input"
        @input="emitChange"
      />

      <!-- Textarea -->
      <textarea
        v-else-if="attr.type === 'textarea'"
        v-model="localValues[attr.id]"
        :required="attr.required"
        :placeholder="attr.placeholder"
        rows="3"
        class="input"
        @input="emitChange"
      />

      <!-- Select -->
      <Select
        v-else-if="attr.type === 'select'"
        v-model="localValues[attr.id]"
        :options="attr.options || []"
        @change="emitChange"
      />

      <!-- Multi-Select (Checkboxes) -->
      <div v-else-if="attr.type === 'multi_select'" class="space-y-2">
        <label v-for="opt in attr.options" :key="opt.value" class="flex items-center gap-2">
          <input
            type="checkbox"
            :value="opt.value"
            :checked="isOptionSelected(attr.id, opt.value)"
            @change="toggleMultiSelectOption(attr.id, opt.value)"
            class="h-4 w-4 rounded-compact border-gray-300 text-primary-600"
          />
          <span class="text-sm text-gray-700 dark:text-gray-300">{{ opt.label }}</span>
        </label>
      </div>

      <!-- Description -->
      <p v-if="attr.description" class="input-hint">{{ attr.description }}</p>
    </div>
  </div>

  <!-- Loading State -->
  <ContentSkeleton v-else-if="loading" variant="form" :rows="3" class="py-4" />
</template>

<script setup lang="ts">
import ContentSkeleton from '@/components/common/ContentSkeleton.vue'
import { ref, watch, onMounted } from 'vue'
import { adminAPI } from '@/api/admin'
import type {
  UserAttributeDefinition,
  UserAttributeValuesMap,
} from '@/api/admin/userAttributes.types'
import Select from '@/components/common/Select.vue'

// UserAttributeForm.vue renders enabled custom user attributes and emits their edited
// values. Existing users load values by attribute ID, while a missing user ID clears
// the local draft. Multi-select choices are stored as JSON strings and converted into
// arrays for checkbox selection and updates.

// Props supplies the optional existing user and the parent's editable attribute map.
interface Props {
  userId?: number // Existing account identifier.
  // modelValue belongs to the parent; this editor emits replacements when local edits
  // need to update that parent form.
  modelValue: UserAttributeValuesMap
}

// Emits publishes attribute values for the parent form to persist on submission.
interface Emits {
  (e: 'update:modelValue', value: UserAttributeValuesMap): void // Publishes the edited parent model.
}

// props and emit connect the selected user and parent-owned attribute map to this
// editor's local input values and change notifications.
const props = defineProps<Props>()
const emit = defineEmits<Emits>()

// loading and attributes control definition feedback and rendered fields; localValues
// stores the editable values keyed by definition ID for emitted updates.
const loading = ref(false)
const attributes = ref<UserAttributeDefinition[]>([])
const localValues = ref<UserAttributeValuesMap>({})

// loadAttributes fetches enabled definitions for the generated form controls. Failures
// are logged and keep existing definitions available, while loading feedback ends after
// either a successful response or rejection.
const loadAttributes = async () => {
  loading.value = true
  try {
    attributes.value = await adminAPI.userAttributes.listEnabledDefinitions()
  } catch (error) {
    // Definition loading failures retain the available fields and are logged, allowing
    // the current editor to remain visible while loading feedback ends.
    console.error('Failed to load attributes:', error)
  } finally {
    loading.value = false
  }
}

// loadUserValues fetches values for a truthy user ID and rebuilds their attribute-ID
// map. It publishes that map locally and to the parent; failures are logged and leave
// current values available.
const loadUserValues = async () => {
  if (!props.userId) return

  try {
    const values = await adminAPI.userAttributes.getUserAttributeValues(props.userId)
    const valuesMap: UserAttributeValuesMap = {}
    values.forEach((v) => {
      valuesMap[v.attribute_id] = v.value
    })
    localValues.value = { ...valuesMap }
    emit('update:modelValue', localValues.value)
  } catch (error) {
    // A failed value request leaves the local map intact and reports the failure in the
    // console instead of emitting an empty replacement.
    console.error('Failed to load user attribute values:', error)
  }
}

// emitChange sends a shallow copy of the local attribute values to the parent.
const emitChange = () => {
  emit('update:modelValue', { ...localValues.value })
}

// isOptionSelected parses a stored multi-select value and checks membership in its
// array. Missing, malformed, or non-array values return false, allowing the checkbox to
// render safely with incomplete stored data.
const isOptionSelected = (attrId: number, optionValue: string): boolean => {
  const value = localValues.value[attrId]
  if (!value) return false
  try {
    const arr = JSON.parse(value)
    return Array.isArray(arr) && arr.includes(optionValue)
  } catch {
    // Malformed stored JSON is treated as an unselected checkbox so the form can render
    // the remaining attributes and accept another selection.
    return false
  }
}

// toggleMultiSelectOption toggles one choice in a stored JSON array and emits the
// edited map. Missing, malformed, or non-array values start from an empty selection so
// the next edit produces valid array JSON.
const toggleMultiSelectOption = (attrId: number, optionValue: string) => {
  let arr: string[] = []
  const value = localValues.value[attrId]
  if (value) {
    try {
      arr = JSON.parse(value)
      // A parsed non-array cannot support checkbox membership, so discard it and start
      // a valid array before applying the requested option toggle.
      if (!Array.isArray(arr)) arr = []
    } catch {
      // Malformed multi-select JSON becomes an empty selection before toggling,
      // allowing the user's next edit to replace it with a valid serialized array.
      arr = []
    }
  }

  const index = arr.indexOf(optionValue)
  if (index > -1) {
    arr.splice(index, 1)
  } else {
    arr.push(optionValue)
  }

  // Attribute values use serialized strings at the API, so convert checkbox choices
  // back into JSON before emitting the edited form map.
  localValues.value[attrId] = JSON.stringify(arr)
  emitChange()
}

// Parent model updates replace the local map only when nonempty; a missing user ID
// separately clears it through the user watcher.
watch(
  () => props.modelValue,
  (newVal) => {
    if (newVal && Object.keys(newVal).length > 0) {
      localValues.value = { ...newVal }
    }
  },
  {
    immediate: true, // Runs on watcher registration.
  },
)

// A truthy selected user starts a value request; clearing the ID resets the draft so
// creation starts without the preceding account values.
watch(
  () => props.userId,
  (newUserId) => {
    if (newUserId) {
      loadUserValues()
    } else {
      localValues.value = {}
    }
  },
  {
    immediate: true, // Runs on watcher registration.
  },
)

// Load the enabled definition catalog after mounting so the form can choose input
// controls independently of the selected user's value request.
onMounted(() => {
  loadAttributes()
})
</script>
