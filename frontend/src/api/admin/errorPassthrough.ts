import { apiClient } from '../client'

/**
 * errorPassthrough.ts manages gateway error-response rules from administrator
 * forms. Rule payloads keep matching criteria, response overrides and monitoring
 * choices together, and the enabled toggle delegates to the update operation
 * with just the enabled field.
 */

/**
 * ErrorPassthroughRule stores error matching criteria and the response or
 * monitoring actions applied on a match.
 */
export interface ErrorPassthroughRule {
  id: number // Record identifier value.
  name: string // Human-readable display name.
  enabled: boolean // Feature activation flag.
  priority: number // Ascending matching priority.
  // error_codes, keywords, and match_mode combine configured condition categories.
  // Empty categories do not participate, and at least one category is required;
  // any/all combines the categories that remain configured.
  error_codes: number[] // Accepted upstream status values.
  // Case-insensitive substrings retain supplied whitespace; an empty configured
  // keyword matches any converted body rather than acting as an omitted category.
  keywords: string[]
  match_mode: 'any' | 'all' // Condition category combination mode.
  // An empty platform list matches all platforms; this filter applies independently
  // of match_mode and combines case-folded labels without trimming their whitespace.
  platforms: string[]
  // passthrough_code retains the upstream status when enabled. Otherwise response_code
  // must supply a positive replacement; rule validation does not impose an HTTP-range check.
  passthrough_code: boolean // Preserve upstream response status.
  response_code: number | null // Replacement response status.
  // passthrough_body retains the extracted error message, not the raw HTTP body.
  // When disabled, custom_message must provide a nonempty string whose whitespace is retained.
  passthrough_body: boolean // Preserve extracted error message.
  custom_message: string | null // Replacement error message.
  // Suppresses ordinary Ops errors for matched requests; provider-health classification
  // and other record paths continue to use their independent consumer policies.
  skip_monitoring: boolean
  description: string | null // Human-readable record description.
  created_at: string // Record creation timestamp.
  updated_at: string // Last modification timestamp.
}

/**
 * CreateRuleRequest supplies a new error-rewrite rule's name and any selected
 * matching or response options.
 */
export interface CreateRuleRequest {
  name: string // Human-readable display name.
  enabled?: boolean // Feature activation flag.
  priority?: number // Ascending matching priority.
  // error_codes, keywords, and match_mode combine configured condition categories.
  // Empty categories do not participate, and at least one category is required;
  // any/all combines the categories that remain configured.
  error_codes?: number[] // Accepted upstream status values.
  // Case-insensitive substrings retain supplied whitespace; an empty configured
  // keyword matches any converted body rather than acting as an omitted category.
  keywords?: string[]
  match_mode?: 'any' | 'all' // Condition category combination mode.
  // An empty platform list matches all platforms; this filter applies independently
  // of match_mode and combines case-folded labels without trimming their whitespace.
  platforms?: string[]
  // passthrough_code retains the upstream status when enabled. Otherwise response_code
  // must supply a positive replacement; rule validation does not impose an HTTP-range check.
  passthrough_code?: boolean // Preserve upstream response status.
  response_code?: number | null // Replacement response status.
  // passthrough_body retains the extracted error message, not the raw HTTP body.
  // When disabled, custom_message must provide a nonempty string whose whitespace is retained.
  passthrough_body?: boolean // Preserve extracted error message.
  custom_message?: string | null // Replacement error message.
  // Suppresses ordinary Ops errors for matched requests; provider-health classification
  // and other record paths continue to use their independent consumer policies.
  skip_monitoring?: boolean
  description?: string | null // Human-readable record description.
}

/**
 * UpdateRuleRequest supplies selected fields for editing an existing gateway
 * error-rewrite rule.
 */
export interface UpdateRuleRequest {
  name?: string // Human-readable display name.
  enabled?: boolean // Feature activation flag.
  priority?: number // Ascending matching priority.
  // error_codes, keywords, and match_mode combine the effective condition categories
  // after updating. Empty categories do not participate, and at least one category
  // is required; any/all combines the categories that remain configured.
  error_codes?: number[] // Accepted upstream status values.
  // Case-insensitive substrings retain supplied whitespace; an empty configured
  // keyword matches any converted body rather than acting as an omitted category.
  keywords?: string[]
  match_mode?: 'any' | 'all' // Condition category combination mode.
  // An empty platform list matches all platforms; this filter applies independently
  // of match_mode and combines case-folded labels without trimming their whitespace.
  platforms?: string[]
  // passthrough_code retains the upstream status when enabled. Otherwise response_code
  // must supply a positive replacement; rule validation does not impose an HTTP-range check.
  passthrough_code?: boolean // Preserve upstream response status.
  response_code?: number | null // Replacement response status.
  // passthrough_body retains the extracted error message, not the raw HTTP body.
  // When disabled, custom_message must provide a nonempty string whose whitespace is retained.
  passthrough_body?: boolean // Preserve extracted error message.
  custom_message?: string | null // Replacement error message.
  // Suppresses ordinary Ops errors for matched requests; provider-health classification
  // and other record paths continue to use their independent consumer policies.
  skip_monitoring?: boolean
  description?: string | null // Human-readable record description.
}

/**
 * list lists all error passthrough rules and returns the server result.
 */
export async function list(): Promise<ErrorPassthroughRule[]> {
  const { data } = await apiClient.get<ErrorPassthroughRule[]>('/admin/error-passthrough-rules')
  return data
}

/**
 * create creates new rule and returns the server result.
 */
export async function create(ruleData: CreateRuleRequest): Promise<ErrorPassthroughRule> {
  const { data } = await apiClient.post<ErrorPassthroughRule>(
    '/admin/error-passthrough-rules',
    ruleData,
  )
  return data
}

/**
 * update updates rule and returns the server result.
 */
export async function update(
  id: number,
  updates: UpdateRuleRequest,
): Promise<ErrorPassthroughRule> {
  const { data } = await apiClient.put<ErrorPassthroughRule>(
    `/admin/error-passthrough-rules/${id}`,
    updates,
  )
  return data
}

/**
 * deleteRule deletes rule and returns the server result.
 */
export async function deleteRule(id: number): Promise<{
  message: string // Server-provided result message.
}> {
  const { data } = await apiClient.delete<{
    message: string // Server-provided result message.
  }>(`/admin/error-passthrough-rules/${id}`)
  return data
}

/**
 * toggleEnabled sets rule enabled status and returns the server result.
 */
export async function toggleEnabled(id: number, enabled: boolean): Promise<ErrorPassthroughRule> {
  return update(id, { enabled })
}

/**
 * errorPassthroughAPI groups gateway error-rewrite rule records for callers
 * using the module's named or default API object.
 */
export const errorPassthroughAPI = {
  list,
  create,
  update,
  delete: deleteRule,
  toggleEnabled,
}

export default errorPassthroughAPI
