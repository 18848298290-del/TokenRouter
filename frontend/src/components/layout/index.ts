/*
 * index.ts exports the shared application and authentication layout components.
 * Consumers can import the page shell, sidebar, header, or authentication frame from
 * this entry while each component retains its own rendering and lifecycle.
 */

export { default as AppLayout } from './AppLayout.vue'
export { default as AppSidebar } from './AppSidebar.vue'
export { default as AppHeader } from './AppHeader.vue'
export { default as AuthLayout } from './AuthLayout.vue'
