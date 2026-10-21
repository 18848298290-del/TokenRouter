<template>
  <ExportProgressDialog
    :show="show"
    :progress="progress"
    :current="current"
    :total="total"
    :estimated-time="estimatedTime"
    @cancel="$emit('cancel')"
  />
</template>

<script setup lang="ts">
import ExportProgressDialog from '@/components/common/ExportProgressDialog.vue'

// UsageExportProgress.vue passes administrator export progress into the shared progress
// dialog. The parent owns export execution and cancellation; this component forwards the
// cancel event and displays the supplied count, percentage, and time estimate.

// The progress inputs belong to the parent export operation; this wrapper forwards their
// current values into the shared dialog.
defineProps<{
  show: boolean // progress dialog visibility
  progress: number // completed export percentage
  current: number // exported record count
  total: number // expected record count
  estimatedTime: string // formatted remaining time estimate
}>()
// The cancel event delegates interruption to the parent that owns the active export
// controller and pending page requests.
defineEmits(['cancel'])
</script>
