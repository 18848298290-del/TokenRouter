import apiClient from './client'

// requests.ts reads administrator request details and their linked usage records.
// Lifecycle and failure diagnostics belong to the request, while each usage row
// carries its own captured performance and derived metrics, including its producing
// attempt and fixed TTFT selection.

// RequestDetail combines one request lifecycle with separately owned usage and error references.
export interface RequestDetail {
  request_id: string
  parent_request_id?: string
  started_at: string
  finished_at?: string
  method: string
  path: string
  state: string
  error_code?: string
  status_code?: number
  duration_ms?: number
  model?: string
  platform?: string
  legacy: boolean
  pending?: boolean
  usage_write_state?: string // Observed usage persistence result.
  usage_write_error?: string // Bounded persistence failure diagnostic.
  aliases?: { kind: string; value: string }[]
  failure_timings?: Record<string, number> // Failed request stage observations.
  attempts?: { number: number; provider_id?: number; upstream_request_id?: string; status_code?: number; duration_ms?: number | null; outcome?: string; stage?: string; reason?: string; failure_timings?: Record<string, number> }[]
  usage: { id: number; model: string; input_tokens: number; output_tokens: number; actual_cost: number; duration_ms?: number | null; ttft_ms?: number | null; tps?: number | null; timings?: { ttft_mode: string; upstream_stream: boolean; values: Record<string, number> } }[] | null
  errors: { id: number; status_code: number; phase: string }[] | null
  audit_ids?: number[]
}

// findRequests 调用管理员接口查询请求诊断记录。
export async function findRequests(requestId: string, signal?: AbortSignal) {
  const { data } = await apiClient.get<{ items: RequestDetail[]; has_more: boolean }>('/admin/requests', {
    params: { request_id: requestId },
    signal,
  })
  return data
}
