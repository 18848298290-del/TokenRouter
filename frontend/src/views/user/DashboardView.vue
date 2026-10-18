<template>
  <AppLayout>
    <template #page-heading-meta>
      <UserDashboardLiveStats ref="liveStatsRef" />
    </template>
    <template #page-heading-actions>
      <UserDashboardUsageToolbar :refreshing="refreshing" @refresh="refreshAll" />
    </template>

    <!-- 首次进入时各区块按 --rise-i 依次上移淡入，最后一块播放完后移除入场类 -->
    <div
      class="space-y-4"
      :class="{ 'dash-rise': entering }"
      :style="dashboardMotionVars"
      data-testid="dashboard-root"
      @animationend="onRiseEnd"
    >
      <UserDashboardUsageChart ref="usageChartRef" />
      <div class="dash-rise-item" :style="{ '--rise-i': 2 }">
        <UserDashboardHeatmap ref="heatmapRef" @select-day="revealChart" />
      </div>
      <div class="dash-rise-item grid grid-cols-1 gap-4 lg:grid-cols-3" :style="{ '--rise-i': 3 }" data-rise-last="true">
        <div class="lg:order-2 lg:col-span-1"><UserDashboardQuickActions /></div>
        <div class="lg:order-1 lg:col-span-2"><UserDashboardAnnouncements /></div>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { useLocaleRefresh } from '@/composables/useLocaleRefresh'
import { onMounted, ref } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { useAnnouncementStore } from '@/stores/announcements'
import AppLayout from '@/components/layout/AppLayout.vue'
import UserDashboardUsageChart from '@/components/user/dashboard/UserDashboardUsageChart.vue'
import UserDashboardUsageToolbar from '@/components/user/dashboard/UserDashboardUsageToolbar.vue'
import UserDashboardLiveStats from '@/components/user/dashboard/UserDashboardLiveStats.vue'
import { provideUsageChartState } from '@/components/user/dashboard/usageChartState'
import { dashboardMotionVars } from '@/components/user/dashboard/dashboardMotion'
import UserDashboardHeatmap from '@/components/user/dashboard/UserDashboardHeatmap.vue'
import UserDashboardAnnouncements from '@/components/user/dashboard/UserDashboardAnnouncements.vue'
import UserDashboardQuickActions from '@/components/user/dashboard/UserDashboardQuickActions.vue'

// DashboardView.vue arranges usage analysis, activity, and account shortcuts in
// the user dashboard. The page provides the shared usage selection consumed by
// its heading toolbar and trend chart, while live statistics and the heatmap
// retain their own refresh lifecycles. Manual refresh waits for those independent
// sections before restoring the refresh control, and each section handles its
// own request failures.
//
// Quick actions precede announcements in document order so narrow screens and
// keyboard navigation reach account tasks first. The desktop grid places the
// announcements beside those actions, and initial section animations share the
// timing variables supplied by the page root.

const authStore = useAuthStore()
const announcementStore = useAnnouncementStore()

// 用量状态由页面提供，标题行的工具栏和正文的图表共用同一份。
const usageState = provideUsageChartState()
const heatmapRef = ref<InstanceType<typeof UserDashboardHeatmap> | null>(null)
const liveStatsRef = ref<InstanceType<typeof UserDashboardLiveStats> | null>(null)
const usageChartRef = ref<InstanceType<typeof UserDashboardUsageChart> | null>(null)
const refreshing = ref(false)
// 入场动画只在首次挂载时播放
const entering = ref(true)

// refreshUser 刷新账户信息，顶栏余额随之更新。
const refreshUser = async () => {
  try {
    await authStore.refreshUser()
  } catch (error) {
    console.error('Failed to refresh user:', error)
  }
}

// App 负责首次预加载；用户主动刷新时同时绕过公告节流获取最新内容。
const refreshAll = async () => {
  refreshing.value = true
  try {
    // 各区块自行处理错误，这里只等全部结束再恢复刷新按钮。
    await Promise.allSettled([
      refreshUser(),
      usageState.load(),
      heatmapRef.value?.reload(),
      liveStatsRef.value?.reload(),
      announcementStore.fetchAnnouncements(true),
    ])
  } finally {
    refreshing.value = false
  }
}

useLocaleRefresh(() => Promise.all([usageState.loadFilterOptions(), usageState.load()]))

// revealChart 在热力图选中某天后，把趋势图滚动到可见区域。
const revealChart = () => {
  const card = usageChartRef.value?.chartCardRef
  if (!card) return
  const reduced = typeof window.matchMedia === 'function' && window.matchMedia('(prefers-reduced-motion: reduce)').matches
  card.scrollIntoView({ behavior: reduced ? 'auto' : 'smooth', block: 'center' })
}

// 最后一块入场结束后移除入场类，之后的重新渲染不会再次播放。
const onRiseEnd = (event: AnimationEvent) => {
  if ((event.target as HTMLElement).dataset?.riseLast) entering.value = false
}

onMounted(() => {
  void refreshUser()
  void usageState.load()
  void usageState.loadFilterOptions()
})
</script>

<style scoped>
/* 区块入场：自下方 8px 上移并淡入，相邻区块错开一个步长。
   只在延迟期间保持起始帧，结束后不保留 transform，避免影响内部 fixed 浮层的定位。 */
.dash-rise :deep(.dash-rise-item) {
  animation: dash-rise var(--dash-rise-ms) var(--motion-ease) backwards;
  animation-delay: calc(var(--rise-i, 0) * var(--dash-rise-step-ms));
}

@keyframes dash-rise {
  from {
    opacity: 0;
    transform: translateY(8px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

@media (prefers-reduced-motion: reduce) {
  .dash-rise :deep(.dash-rise-item) {
    animation: none;
  }
}
</style>
