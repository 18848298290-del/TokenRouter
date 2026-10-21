import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import i18n, { initI18n, getLocale } from './i18n'
import { useAppStore } from '@/stores/app'
import { initTheme } from '@/composables/useTheme'
import { updateFavicon } from '@/utils/branding'
import { isIOSDevice } from '@/utils/device'
import '@fontsource-variable/plus-jakarta-sans'
import '@fontsource-variable/plus-jakarta-sans/wght-italic.css'
import '@fontsource-variable/geist-mono'
import '@fontsource-variable/noto-sans-sc'
import './style.css'

// main.ts initializes browser presentation and mounts the Vue application after its
// initial route is ready. Font imports precede the global stylesheet, and theme
// selection runs before mounting so the first rendered view uses the selected
// appearance.
//
// Pinia is installed before reading injected site settings. Branding is applied before
// language initialization, then public settings are refreshed if their locale differs
// from the active language. Router installation follows those steps so its initial
// access checks can read the prepared store. The application waits for initial
// navigation before mounting the shared shell and its page component.

// initIOSViewportZoomFix adds maximum-scale to the existing viewport declaration on iOS
// devices. Missing viewport metadata or an existing maximum-scale value is left alone;
// other devices keep their configured viewport behavior.
function initIOSViewportZoomFix() {
  if (!isIOSDevice()) return

  const viewport = document.querySelector('meta[name="viewport"]')
  if (!viewport) return

  const content = viewport.getAttribute('content') || ''
  if (/maximum-scale/i.test(content)) return
  viewport.setAttribute('content', [content.trim(), 'maximum-scale=1.0'].filter(Boolean).join(', '))
}

// bootstrap initializes browser appearance and installs the application dependencies
// before mounting. Pinia precedes injected settings so branding and access flags are
// available during startup. Language initialization then checks whether those settings
// need a locale refresh before router installation starts navigation. The router and
// translation plugin are installed before awaiting route readiness. Only after that
// navigation completes does the application mount its shell and routed view.
async function bootstrap() {
  initTheme()
  initIOSViewportZoomFix()

  const app = createApp(App)
  const pinia = createPinia()
  app.use(pinia)
  const appStore = useAppStore()
  // Read injected settings after Pinia installation and before router installation,
  // allowing initial navigation and branding to observe startup configuration.
  appStore.initFromInjectedConfig()
  if (appStore.siteName && appStore.siteName !== 'TokenRouter') {
    document.title = `${appStore.siteName} - AI API Gateway`
  }
  updateFavicon(appStore.siteLogo)

  // Complete language initialization before checking configuration freshness; settings
  // translated for another locale are refreshed before the initial route starts.
  await initI18n()
  if (appStore.cachedPublicSettings?.locale !== getLocale())
    await appStore.fetchPublicSettings(true)

  app.use(router)
  app.use(i18n)
  // Wait until initial navigation resolves so the first mount renders its destination
  // with prepared authentication, settings, and translated route metadata.
  await router.isReady()
  app.mount('#app')
}

bootstrap()
