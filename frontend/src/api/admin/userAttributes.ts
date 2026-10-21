import { apiClient } from '../client'
import type {
  UserAttributeDefinition,
  UserAttributeValue,
  CreateUserAttributeRequest,
  UpdateUserAttributeRequest,
  UserAttributeValuesMap,
} from '@/api/admin/userAttributes.types'

/**
 * userAttributes.ts manages custom attribute definitions and user-assigned
 * values. Enabled-definition reads share the definition list endpoint with an
 * enabled filter, while batch value lookup returns values grouped by user and
 * attribute identifiers for administrator tables.
 */

/**
 * listDefinitions retrieves all attribute definitions and returns the server
 * result.
 */
export async function listDefinitions(): Promise<UserAttributeDefinition[]> {
  const { data } = await apiClient.get<UserAttributeDefinition[]>('/admin/user-attributes')
  return data
}

/**
 * listEnabledDefinitions retrieves enabled attribute definitions only and
 * returns the server result.
 */
export async function listEnabledDefinitions(): Promise<UserAttributeDefinition[]> {
  const { data } = await apiClient.get<UserAttributeDefinition[]>('/admin/user-attributes', {
    params: { enabled: true },
  })
  return data
}

/**
 * createDefinition creates a new attribute definition and returns the server
 * result.
 */
export async function createDefinition(
  request: CreateUserAttributeRequest,
): Promise<UserAttributeDefinition> {
  const { data } = await apiClient.post<UserAttributeDefinition>('/admin/user-attributes', request)
  return data
}

/**
 * updateDefinition updates an attribute definition and returns the server
 * result.
 */
export async function updateDefinition(
  id: number,
  request: UpdateUserAttributeRequest,
): Promise<UserAttributeDefinition> {
  const { data } = await apiClient.put<UserAttributeDefinition>(
    `/admin/user-attributes/${id}`,
    request,
  )
  return data
}

/**
 * deleteDefinition deletes an attribute definition and returns the server
 * result.
 */
export async function deleteDefinition(id: number): Promise<{
  message: string // Server-provided result message.
}> {
  const { data } = await apiClient.delete<{
    message: string // Server-provided result message.
  }>(`/admin/user-attributes/${id}`)
  return data
}

/**
 * getUserAttributeValues retrieves user's attribute values and returns the
 * server result.
 */
export async function getUserAttributeValues(userId: number): Promise<UserAttributeValue[]> {
  const { data } = await apiClient.get<UserAttributeValue[]>(`/admin/users/${userId}/attributes`)
  return data
}

/**
 * updateUserAttributeValues updates user's attribute values (batch) and returns
 * the server result.
 */
export async function updateUserAttributeValues(
  userId: number,
  values: UserAttributeValuesMap,
): Promise<{
  message: string // Server-provided result message.
}> {
  const { data } = await apiClient.put<{
    message: string // Server-provided result message.
  }>(`/admin/users/${userId}/attributes`, {
    values,
  })
  return data
}

/**
 * BatchUserAttributesResponse maps user identifiers to attribute-ID and
 * string-value maps for administrator tables.
 */
export interface BatchUserAttributesResponse {
  attributes: Record<number, Record<number, string>> // User-ID to attribute-ID value maps.
}

/**
 * getBatchUserAttributes retrieves attribute values for multiple users and
 * returns the server result.
 */
export async function getBatchUserAttributes(
  userIds: number[],
): Promise<BatchUserAttributesResponse> {
  const { data } = await apiClient.post<BatchUserAttributesResponse>(
    '/admin/user-attributes/batch',
    { user_ids: userIds },
  )
  return data
}

/**
 * userAttributesAPI groups custom user attribute definitions and values for
 * callers using the module's named or default API object.
 */
export const userAttributesAPI = {
  listDefinitions,
  listEnabledDefinitions,
  createDefinition,
  updateDefinition,
  deleteDefinition,
  getUserAttributeValues,
  updateUserAttributeValues,
  getBatchUserAttributes,
}

export default userAttributesAPI
