import type { GroupOpenAIFastPolicy } from '@/api/groups.types'

// groupsOpenAIFast.ts normalizes the group-level OpenAI Fast policy before form submission.
// Supported values pass through to the group configuration, and unknown values follow the
// incoming request so legacy or empty drafts receive a recognized policy.

// normalizeGroupOpenAIFastPolicy returns a recognized group-level Fast policy. Unsupported or
// empty strings select follow_request; applicable provider and protocol execution paths
// determine how that stored policy affects requests.
export function normalizeGroupOpenAIFastPolicy(policy: string): GroupOpenAIFastPolicy {
  if (['follow_request', 'force_priority', 'force_ultrafast', 'force_off'].includes(policy)) {
    return policy as GroupOpenAIFastPolicy
  }
  return 'follow_request'
}
