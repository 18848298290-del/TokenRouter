/// <reference types="vite/client" />

// vite-env.d.ts declares environment values and Vue component imports available to the
// frontend compiler. Vite supplies browser build metadata, and the Vue module
// declaration lets TypeScript treat single-file components as importable definitions
// when checking application modules.

// ImportMetaEnv describes build-time paths supplied to frontend modules by Vite.
interface ImportMetaEnv {
  readonly VITE_API_BASE_URL: string // Configured HTTP API root.
  readonly BASE_URL: string // Vite application base path.
}

// ImportMeta exposes the typed environment object attached to each frontend module.
interface ImportMeta {
  readonly env: ImportMetaEnv // Typed build environment values.
}

declare module '*.vue' {
  import type { DefineComponent } from 'vue'
  // component represents an imported Vue single-file component for application type
  // checking and composition by TypeScript consumers.
  const component: DefineComponent<{}, {}, any>
  export default component
}
