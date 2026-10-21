import { apiClient } from '../client'
import type {
  ScheduledTestPlan,
  ScheduledTestResult,
  CreateScheduledTestPlanRequest,
  UpdateScheduledTestPlanRequest,
} from '@/api/admin/scheduledTests.types'

/**
 * scheduledTests.ts manages recurring provider test plans and their recorded
 * results. Plan operations select a provider or plan identifier, while result
 * history accepts its requested limit and missing plan or result collections
 * become empty lists for callers.
 */

/**
 * listByProvider retrieves scheduled test plans belonging to the selected
 * provider. A null or undefined response becomes an empty list so plan consumers
 * can iterate the result directly.
 */
export async function listByProvider(providerId: number): Promise<ScheduledTestPlan[]> {
  const { data } = await apiClient.get<ScheduledTestPlan[]>(
    `/admin/providers/${providerId}/scheduled-test-plans`,
  )
  // A missing collection represents no returned entries for this view; expose
  // an empty array so plan and history consumers can iterate it directly.
  return data ?? []
}

/**
 * create creates a new scheduled test plan and returns the server result.
 */
export async function create(req: CreateScheduledTestPlanRequest): Promise<ScheduledTestPlan> {
  const { data } = await apiClient.post<ScheduledTestPlan>('/admin/scheduled-test-plans', req)
  return data
}

/**
 * update updates an existing scheduled test plan and returns the server result.
 */
export async function update(
  id: number,
  req: UpdateScheduledTestPlanRequest,
): Promise<ScheduledTestPlan> {
  const { data } = await apiClient.put<ScheduledTestPlan>(`/admin/scheduled-test-plans/${id}`, req)
  return data
}

/**
 * deletePlan awaits removal of one scheduled test plan without returning response
 * data to its caller.
 */
export async function deletePlan(id: number): Promise<void> {
  await apiClient.delete(`/admin/scheduled-test-plans/${id}`)
}

/**
 * listResults retrieves recorded results for one scheduled test plan. A truthy
 * limit is forwarded in query parameters, and a null or undefined response
 * becomes an empty list for history consumers.
 */
export async function listResults(planId: number, limit?: number): Promise<ScheduledTestResult[]> {
  const { data } = await apiClient.get<ScheduledTestResult[]>(
    `/admin/scheduled-test-plans/${planId}/results`,
    {
      params: limit ? { limit } : undefined,
    },
  )
  // A missing collection represents no returned entries for this view; expose
  // an empty array so plan and history consumers can iterate it directly.
  return data ?? []
}

/**
 * scheduledTestsAPI groups provider scheduled-test plans and results for callers
 * using the module's named or default API object.
 */
export const scheduledTestsAPI = {
  listByProvider,
  create,
  update,
  delete: deletePlan,
  listResults,
}

export default scheduledTestsAPI
