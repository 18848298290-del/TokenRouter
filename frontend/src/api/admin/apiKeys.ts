import { apiClient } from '../client'
import type { ApiKey } from '@/api/keys.types'

/**
 * apiKeys.ts updates API-key group bindings from administrator views. The group
 * update translates null into the server's zero unbind value and returns both
 * the updated key and any group-access grant metadata supplied by the endpoint.
 */

/**
 * UpdateApiKeyGroupResult pairs the updated API key with metadata about any
 * automatically granted group access.
 */
export interface UpdateApiKeyGroupResult {
  api_key: ApiKey // Gateway API credential.
  auto_granted_group_access: boolean // Auto granted group access.
  granted_group_id?: number // Granted group ID.
  granted_group_name?: string // Granted group name.
}

/**
 * updateApiKeyGroup updates the selected key's group binding and returns grant
 * metadata. A null group is encoded as zero to unbind the key; other numeric
 * group values are forwarded unchanged.
 */
export async function updateApiKeyGroup(
  id: number,
  groupId: number | null,
): Promise<UpdateApiKeyGroupResult> {
  const { data } = await apiClient.put<UpdateApiKeyGroupResult>(`/admin/api-keys/${id}`, {
    group_id: groupId === null ? 0 : groupId,
  })
  return data
}

/**
 * apiKeysAPI groups administrator API-key group bindings for callers using the
 * module's named or default API object.
 */
export const apiKeysAPI = {
  updateApiKeyGroup,
}

export default apiKeysAPI
