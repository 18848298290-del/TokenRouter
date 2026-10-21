import type { GroupRoutingPolicy } from '@/api/groups.types'

// routingPolicy.ts creates group routing policy drafts and copies stored policies for editing.
// Fresh nested collections isolate form edits, while cloning fills missing settings and keeps
// additional stored properties. Every returned policy enables application of the configured
// routing rules.

// defaultRoutingPolicy returns a fresh enabled routing draft with empty model rules. Model
// restrictions start disabled, use the group-mapped model identity, and receive separate
// mapping, allowlist, and feature collections.
export function defaultRoutingPolicy(): GroupRoutingPolicy {
  return {
    enabled: true,
    model_mapping: {},
    restrict_models: false,
    restriction_model_source: 'group_mapped',
    allowed_models: [],
    features: '',
    features_config: {},
  }
}

// cloneRoutingPolicy returns a JSON-cloned policy merged with fresh draft defaults. Missing
// input creates the default policy, missing collections are replaced, and enabled is set to
// true for editing and submission.
export function cloneRoutingPolicy(policy?: GroupRoutingPolicy): GroupRoutingPolicy {
  const copied = policy
    ? (JSON.parse(JSON.stringify(policy)) as GroupRoutingPolicy)
    : defaultRoutingPolicy()
  return {
    ...defaultRoutingPolicy(),
    ...copied,
    enabled: true,
    model_mapping: copied.model_mapping ?? {},
    allowed_models: copied.allowed_models ?? [],
    features_config: copied.features_config ?? {},
    features: copied.features ?? '',
  }
}
