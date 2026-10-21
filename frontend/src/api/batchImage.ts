import { getLocale } from '@/i18n'
import { buildGatewayUrl } from './client'

/**
 * batchImage.ts sends image-batch gateway requests using a caller-supplied API
 * key and current locale. These fetch calls decode gateway JSON or binary bodies
 * directly, with HTTP failures converted into errors carrying status, code and
 * request ID. Download consumers receive Blobs and can invoke the browser-save
 * helper.
 */

/**
 * BatchImageStatus retains known batch execution and storage states while
 * accepting additional gateway status strings.
 */
export type BatchImageStatus =
  | 'queued'
  | 'running'
  | 'indexing'
  | 'processing_results'
  | 'settling'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'output_deleted'
  | string

/**
 * BatchImageSubmitItem pairs a caller-defined item identity with its prompt and
 * optional count or reference images.
 */
export interface BatchImageSubmitItem {
  custom_id: string // Recorded custom id.
  prompt: string // Submitted generation prompt.
  output_count?: number // Requested image count.
  reference_images?: BatchImageReferenceImage[] // Recorded reference images.
}

/**
 * BatchImageReferenceImage describes a reference image using MIME metadata and
 * optional inline data or a file URI.
 */
export interface BatchImageReferenceImage {
  id?: string // Record identifier value.
  type?: string // Represented record category.
  mime_type: string // Content MIME type.
  data?: string // Submitted base64 image bytes, without a data-URL prefix.
  file_uri?: string // Recorded file uri.
}

/**
 * BatchImageSubmitRequest selects the batch model and image options together
 * with submitted items and optional metadata.
 */
export interface BatchImageSubmitRequest {
  model: string // Model identifier value.
  task_name?: string // Requested task name.
  parent_batch_id?: string // Parent batch ID.
  platform?: '' | 'gemini_api' | 'vertex' | string // Provider platform identifier.
  image_size?: '1K' | '2K' | '4K' | string // Requested image size.
  response_mime_type?: string // Response mime type.
  aspect_ratio?: string // Requested aspect ratio.
  items: BatchImageSubmitItem[] // Submitted batch image requests.
  metadata?: Record<string, string> // Additional structured metadata.
}

/**
 * BatchImageJob describes batch execution totals, held or settled costs and
 * output-download lifecycle timestamps.
 */
export interface BatchImageJob {
  id: string // Record identifier value.
  object: string // Gateway object discriminator.
  task_name: string // Recorded task name.
  parent_batch_id?: string | null // Parent batch ID.
  status: BatchImageStatus // Current server-reported status.
  model: string // Model identifier value.
  platform: string // Provider platform identifier.
  item_count: number // Recorded item count.
  success_count: number // Recorded success count.
  fail_count: number // Recorded fail count.
  estimated_cost: number // Estimated task cost.
  hold_amount: number // Reserved task credit.
  actual_cost: number | null // Actual deducted cost.
  created_at: number // Creation time, Unix seconds.
  submitted_at: number | null // Submission time, Unix seconds.
  settled_at: number | null // Settlement time, Unix seconds.
  downloaded_at?: number | null // Download time, Unix seconds.
  output_deleted_at?: number | null // Output deletion time, Unix seconds.
}

/**
 * BatchImageItem reports one caller-identified item's outcome, image metadata
 * and optional provider or system failure.
 */
export interface BatchImageItem {
  batch_id?: string // Recorded batch id.
  source_task_name?: string // Source task name.
  custom_id: string // Recorded custom id.
  status: string // Current server-reported status.
  prompt_preview?: string | null // Recorded prompt preview.
  mime_type: string | null // Content MIME type.
  file_extension: string | null // Recorded file extension.
  image_count: number // Recorded image count.
  error?: {
    code: string // Server result code.
    message: string // Server-provided result message.
    source?: 'provider' | 'system' | string // Recorded observation source.
  } | null // Operation failure detail.
}

/**
 * BatchImageItemsResponse contains one page of batch-item outcomes and a
 * continuation availability flag.
 */
export interface BatchImageItemsResponse {
  object: string // Gateway object discriminator.
  data: BatchImageItem[] // Returned response data.
  has_more: boolean // More-results availability flag.
}

/**
 * BatchImageJobsResponse contains one page of batch jobs and a continuation
 * availability flag.
 */
export interface BatchImageJobsResponse {
  object: string // Gateway object discriminator.
  data: BatchImageJob[] // Returned response data.
  has_more: boolean // More-results availability flag.
}

/**
 * BatchImageModel identifies a gateway model available for image batches and its
 * platform.
 */
export interface BatchImageModel {
  id: string // Record identifier value.
  object: string // Gateway object discriminator.
  platform: string // Provider platform identifier.
}

/**
 * BatchImageModelsResponse wraps the available batch model list in the gateway
 * object response.
 */
export interface BatchImageModelsResponse {
  object: string // Gateway object discriminator.
  data: BatchImageModel[] // Returned response data.
}

/**
 * BatchImageJobsListOptions carries gateway job filters and opaque cursor
 * pagination. The list helper maps taskName to task_name and omits falsy filter
 * values when constructing URL parameters.
 */
export interface BatchImageJobsListOptions {
  limit?: number // Maximum returned entries.
  cursor?: string // Opaque continuation cursor.
  status?: string // Current server-reported status.
  taskName?: string // Recorded task name.
  downloaded?: '' | 'true' | 'false' | string // Download-state query filter.
  from?: string // Requested interval start.
  to?: string // Requested interval end.
}

/**
 * parseBatchImageError builds an Error from a failed gateway response. Nested
 * error messages take precedence over top-level messages; invalid JSON uses the
 * HTTP status text, and both paths attach status and request ID.
 */
async function parseBatchImageError(response: Response): Promise<Error> {
  try {
    const body = await response.json()
    const message = body?.error?.message || body?.message || response.statusText
    const error = new Error(message)
    ;(error as any).code = body?.error?.code || response.status
    ;(error as any).status = response.status
    ;(error as any).requestId = response.headers.get('X-Request-Id') || ''
    return error
  } catch {
    /**
     * If the gateway body cannot be decoded as JSON, retain HTTP status and
     * request identity so callers still receive a useful failure.
     */
    const error = new Error(response.statusText || `HTTP ${response.status}`)
    ;(error as any).code = response.status
    ;(error as any).status = response.status
    ;(error as any).requestId = response.headers.get('X-Request-Id') || ''
    return error
  }
}

/**
 * authHeaders builds gateway authorization and language headers for the supplied
 * API key. Extra headers are spread last and may override either default, so
 * callers control any deliberate header replacement.
 */
function authHeaders(apiKey: string, extra?: HeadersInit): HeadersInit {
  return {
    Authorization: `Bearer ${apiKey}`,
    'Accept-Language': getLocale(),
    ...extra,
  }
}

/**
 * submitBatchImageJob sends a JSON batch request with the caller's idempotency
 * key. Reuse that key for retries of the same submission, and pass the gateway
 * API key that owns the batch.
 */
export async function submitBatchImageJob(
  apiKey: string,
  payload: BatchImageSubmitRequest,
  idempotencyKey: string,
): Promise<BatchImageJob> {
  const response = await fetch(buildGatewayUrl('/v1/images/batches'), {
    method: 'POST',
    headers: authHeaders(apiKey, {
      'Content-Type': 'application/json',
      'Idempotency-Key': idempotencyKey,
    }),
    body: JSON.stringify(payload),
  })
  if (!response.ok) throw await parseBatchImageError(response)
  return response.json()
}

/**
 * getBatchImageJob retrieves one gateway batch using its owning API key. The
 * batch identifier is URL-encoded, and a failed HTTP response is converted into
 * the structured gateway error used by callers.
 */
export async function getBatchImageJob(apiKey: string, batchId: string): Promise<BatchImageJob> {
  const response = await fetch(
    buildGatewayUrl(`/v1/images/batches/${encodeURIComponent(batchId)}`),
    {
      headers: authHeaders(apiKey),
    },
  )
  if (!response.ok) throw await parseBatchImageError(response)
  return response.json()
}

/**
 * listBatchImageJobs retrieves jobs using a numeric limit or filter object.
 * Numeric input is sent directly; an object uses twenty when limit is falsy and
 * includes other filters only when truthy.
 */
export async function listBatchImageJobs(
  apiKey: string,
  options: number | BatchImageJobsListOptions = 20,
): Promise<BatchImageJobsResponse> {
  const params = new URLSearchParams()
  if (typeof options === 'number') {
    params.set('limit', String(options))
  } else {
    params.set('limit', String(options.limit || 20))
    if (options.cursor) params.set('cursor', options.cursor)
    if (options.status) params.set('status', options.status)
    if (options.taskName) params.set('task_name', options.taskName)
    if (options.downloaded) params.set('downloaded', options.downloaded)
    if (options.from) params.set('from', options.from)
    if (options.to) params.set('to', options.to)
  }
  const response = await fetch(buildGatewayUrl(`/v1/images/batches?${params.toString()}`), {
    headers: authHeaders(apiKey),
  })
  if (!response.ok) throw await parseBatchImageError(response)
  return response.json()
}

/**
 * listBatchImageModels retrieves image-batch model choices authorized by the
 * supplied API key. It returns the gateway JSON body directly, and HTTP failures
 * become errors carrying response status and request identity.
 */
export async function listBatchImageModels(apiKey: string): Promise<BatchImageModelsResponse> {
  const response = await fetch(buildGatewayUrl('/v1/images/batches/models'), {
    headers: authHeaders(apiKey),
  })
  if (!response.ok) throw await parseBatchImageError(response)
  return response.json()
}

/**
 * listBatchImageItems retrieves batch items using the supplied gateway API key.
 * A truthy status adds an encoded status filter; otherwise the request asks for
 * the batch's unfiltered item list.
 */
export async function listBatchImageItems(
  apiKey: string,
  batchId: string,
  status = '',
): Promise<BatchImageItemsResponse> {
  const query = status ? `?status=${encodeURIComponent(status)}` : ''
  const response = await fetch(
    buildGatewayUrl(`/v1/images/batches/${encodeURIComponent(batchId)}/items${query}`),
    {
      headers: authHeaders(apiKey),
    },
  )
  if (!response.ok) throw await parseBatchImageError(response)
  return response.json()
}

/**
 * cancelBatchImageJob requests cancellation of the identified gateway batch. The
 * request uses the supplied owning API key and returns the resulting job record,
 * while failed HTTP responses propagate through gateway error parsing.
 */
export async function cancelBatchImageJob(apiKey: string, batchId: string): Promise<BatchImageJob> {
  const response = await fetch(
    buildGatewayUrl(`/v1/images/batches/${encodeURIComponent(batchId)}/cancel`),
    {
      method: 'POST',
      headers: authHeaders(apiKey),
    },
  )
  if (!response.ok) throw await parseBatchImageError(response)
  return response.json()
}

/**
 * downloadBatchImageZip downloads a batch archive using the supplied gateway API
 * key. The caller receives a Blob and owns saving or disposing of it after HTTP
 * error handling completes.
 */
export async function downloadBatchImageZip(apiKey: string, batchId: string): Promise<Blob> {
  const response = await fetch(
    buildGatewayUrl(`/v1/images/batches/${encodeURIComponent(batchId)}/download`),
    {
      headers: authHeaders(apiKey),
    },
  )
  if (!response.ok) throw await parseBatchImageError(response)
  return response.blob()
}

/**
 * getBatchImageItemContent downloads a selected item's image as a Blob. The
 * image index defaults to zero, and batch, custom-item and index values are
 * URL-encoded before the gateway request.
 */
export async function getBatchImageItemContent(
  apiKey: string,
  batchId: string,
  customId: string,
  imageIndex = 0,
): Promise<Blob> {
  const response = await fetch(
    buildGatewayUrl(
      `/v1/images/batches/${encodeURIComponent(batchId)}/items/${encodeURIComponent(customId)}/content?image_index=${encodeURIComponent(String(imageIndex))}`,
    ),
    {
      headers: authHeaders(apiKey),
    },
  )
  if (!response.ok) throw await parseBatchImageError(response)
  return response.blob()
}

/**
 * deleteBatchImageJobRecord requests deletion of the selected gateway batch
 * record. The caller supplies its API key, and the promise completes after a
 * successful response or rejects with parsed gateway error details.
 */
export async function deleteBatchImageJobRecord(apiKey: string, batchId: string): Promise<void> {
  const response = await fetch(
    buildGatewayUrl(`/v1/images/batches/${encodeURIComponent(batchId)}`),
    {
      method: 'DELETE',
      headers: authHeaders(apiKey),
    },
  )
  if (!response.ok) throw await parseBatchImageError(response)
}

/**
 * saveBlob starts a browser download for a Blob and suggested filename. It
 * creates a temporary anchor and object URL, clicks the anchor, then removes the
 * element and revokes the URL.
 */
export function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  document.body.appendChild(link)
  link.click()
  document.body.removeChild(link)
  /**
   * After the anchor click and removal, release the temporary object URL so the
   * browser does not retain this download Blob reference.
   */
  URL.revokeObjectURL(url)
}
