// index.ts exports shared provider authorization and statistics components from a single
// import entry. Consumers resolve the same component implementations through these named
// exports, and lifecycle ownership remains with each exported component and the dialog
// that mounts it.
export { default as ReAuthProviderModal } from './ReAuthProviderModal.vue'
export { default as OAuthAuthorizationFlow } from './OAuthAuthorizationFlow.vue'
export { default as ProviderStatsModal } from './ProviderStatsModal.vue'
