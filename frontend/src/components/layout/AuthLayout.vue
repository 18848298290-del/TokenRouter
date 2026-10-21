<template>
  <div
    class="ba-theme-shell relative flex min-h-screen items-center justify-center overflow-hidden p-4"
  >
    <!-- Background -->
    <div class="ba-theme-backdrop pointer-events-none fixed inset-0"></div>
    <AuthBackground />

    <!-- Content Container -->
    <div v-content-reveal="motionRoute?.path" class="relative z-10 w-full max-w-md">
      <!-- Logo/Brand -->
      <div class="mb-8 text-center">
        <!-- Custom Logo or Default Logo -->
        <template v-if="settingsLoaded">
          <div
            class="mb-4 inline-flex h-16 w-16 items-center justify-center overflow-hidden rounded-surface shadow-lg shadow-primary-500/30"
          >
            <img :src="siteLogo || '/logo.svg'" alt="Logo" class="h-full w-full object-contain" />
          </div>
          <!-- Brand headings use the shared page-title colors in light and dark themes. -->
          <h1 class="mb-2 page-title">
            {{ siteName }}
          </h1>
          <p class="text-sm text-gray-500 dark:text-dark-400">
            {{ siteSubtitle }}
          </p>
        </template>
      </div>

      <!-- Card Container -->
      <div class="card-glass rounded-surface p-8 shadow-glass">
        <slot />
      </div>

      <!-- Footer Links -->
      <div class="mt-6 text-center text-sm">
        <slot name="footer" />
      </div>

      <!-- Copyright -->
      <div class="mt-8 text-center text-xs text-gray-400 dark:text-dark-500">
        &copy; {{ currentYear }} {{ siteName }}. {{ t('common.rightsReserved') }}
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { vContentReveal } from '@/directives/contentReveal'
import { useRoute as useMotionRoute } from 'vue-router'
import { computed, onMounted } from 'vue'
import AuthBackground from '@/components/auth/AuthBackground.vue'
import { useAppStore } from '@/stores'
import { sanitizeUrl } from '@/utils/url'

// AuthLayout.vue frames authentication forms with site branding, a background, and
// footer slots. Public settings determine the displayed name, sanitized logo, and
// optional subtitle override, while mounting requests settings so the branding can
// appear when available.

// t supplies authentication layout translations using the installed application locale;
// form content and footer links arrive through the component slots.
const { t } = useI18n()

// motionRoute supplies the path used to reveal authentication content again when
// navigation changes between pages that share this layout.
const motionRoute = useMotionRoute()

// appStore supplies cached public branding and its loading status, allowing the layout
// to render branding after settings have been obtained.
const appStore = useAppStore()

// Branding values use configured public settings; the logo passes URL sanitization and
// the subtitle honors an explicit empty override when one is present.
const siteName = computed(() => appStore.siteName || 'TokenRouter')
const siteLogo = computed(() =>
  sanitizeUrl(appStore.siteLogo || '', { allowRelative: true, allowDataUrl: true }),
)
const siteSubtitle = computed(() =>
  appStore.cachedPublicSettings?.site_text_overrides?.includes('site_subtitle')
    ? appStore.cachedPublicSettings.site_subtitle || ''
    : t('home.heroDescription'),
)
const settingsLoaded = computed(() => appStore.publicSettingsLoaded)

// currentYear evaluates the local calendar year when the computed value is first read
// for the copyright line; it has no reactive clock source.
const currentYear = computed(() => new Date().getFullYear())

// Request public settings on mount so the branding block can become visible after the
// store completes its configured loading process.
onMounted(() => {
  appStore.fetchPublicSettings()
})
</script>
