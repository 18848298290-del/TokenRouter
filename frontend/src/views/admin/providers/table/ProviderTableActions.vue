<template>
  <div class="flex flex-wrap items-center gap-2">
    <slot name="before"></slot>
    <button
      @click="$emit('refresh')"
      :disabled="loading"
      class="btn btn-secondary shrink-0 btn-icon"
    >
      <Icon name="refresh" size="sm" :class="[loading ? 'animate-spin' : '']" />
    </button>
    <slot name="after"></slot>
    <slot name="beforeCreate"></slot>
    <button @click="$emit('create')" class="btn btn-primary">
      {{ t('admin.providers.createProvider') }}
    </button>
    <slot name="afterCreate"></slot>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'

/**
 * ProviderTableActions.vue renders refresh and create actions for the provider list toolbar.
 * The parent supplies loading state and handles emitted actions, while this component
 * translates button labels and reflects refresh activity through the icon presentation.
 */

// loading reflects the parent list request and controls refresh feedback.
defineProps(['loading'])
// refresh asks the page to reload providers; create opens the provider creation workflow owned
// by the page.
defineEmits(['refresh', 'create'])

/**
 * The translator supplies the create-button label for the toolbar; the provider page
 * handles the creation action emitted by that button.
 */
const { t } = useI18n()
</script>
