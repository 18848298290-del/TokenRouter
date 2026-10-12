<template>
  <Disclosure
    class="border-t border-gray-200 pt-4 dark:border-dark-600"
    summary-class="flex w-full min-w-0 items-center gap-2 rounded-compact text-left text-gray-500 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:text-dark-400"
  >
    <template #summary>
      <span class="shrink-0 text-sm font-semibold text-gray-900 dark:text-dark-50">{{ title }}</span>
      <span v-if="meta" class="min-w-0 truncate text-xs">{{ meta }}</span>
    </template>
    <div class="pt-4"><slot /></div>
  </Disclosure>
</template>

<script setup lang="ts">
import Disclosure from '@/components/common/Disclosure.vue'

// 规则卡片里的次要分组默认收起，meta 在标题旁显示当前配置，收起时也能看到。
defineProps<{ title: string; meta?: string }>()
</script>
