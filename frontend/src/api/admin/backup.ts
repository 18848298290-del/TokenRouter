import { apiClient } from '../client'

/**
 * backup.ts sends administrator backup configuration and lifecycle requests.
 * Storage, content selection and schedules have separate endpoints, while backup
 * records expose progress and download parts; local downloads are returned as
 * Blobs and restore requests include the administrator password.
 */

/**
 * BackupS3Config defines S3 connection credentials, object prefix and upload
 * options used by backup storage.
 */
export interface BackupS3Config {
  endpoint: string // S3-compatible service endpoint URL.
  region: string // Service deployment region.
  bucket: string // S3 storage bucket name.
  access_key_id: string // Access key ID.
  /**
   * This optional secret is supplied by the backup form when saving storage
   * settings; readback configuration does not populate the password input.
   */
  secret_access_key?: string // S3 secret credential.
  prefix: string // Backup object prefix.
  force_path_style: boolean // Force path style.
  upload_mode: 'multipart' | 'spooled_put' // Configured upload mode.
}

/**
 * BackupStorageType selects local filesystem or S3 storage for administrator
 * backup operations.
 */
export type BackupStorageType = 'local' | 's3'

/**
 * BackupStorageConfig combines the selected storage type with local-path and S3
 * configuration values.
 */
export interface BackupStorageConfig {
  type: BackupStorageType // Represented record category.
  local_path: string // Configured local path.
  s3: BackupS3Config // S3 storage settings.
}

/**
 * BackupContentConfig selects record categories and explicitly excluded table
 * data included in a backup.
 */
export interface BackupContentConfig {
  include_usage_records: boolean // Include usage records.
  include_ops_logs: boolean // Include ops logs.
  include_audit_logs: boolean // Include audit logs.
  include_runtime_data: boolean // Include runtime data.
  excluded_table_data?: string[] // Excluded table data.
}

/**
 * BackupScheduleConfig combines recurring backup scheduling with age and count
 * retention limits.
 */
export interface BackupScheduleConfig {
  enabled: boolean // Feature activation flag.
  cron_expr: string // Configured cron expr.
  retain_days: number // Configured retain days.
  retain_count: number // Configured retain count.
}

/**
 * BackupRecord describes a backup's execution, storage location, optional parts
 * and restore outcome.
 */
export interface BackupRecord {
  id: string // Record identifier value.
  status: 'pending' | 'running' | 'completed' | 'failed' // Current server-reported status.
  backup_type: string // Recorded backup type.
  storage_type?: BackupStorageType // Recorded storage type.
  storage_key?: string // Recorded storage key.
  file_name: string // Recorded file name.
  s3_key: string // Recorded s3 key.
  parts?: BackupPart[] // Backup segment metadata.
  size_bytes: number // Content size bytes.
  triggered_by: string // Recorded triggered by.
  error_message?: string // Recorded error message.
  started_at: string // Operation start timestamp.
  finished_at?: string // Operation completion timestamp.
  expires_at?: string // Recorded expiration timestamp.
  progress?: string // Backup progress description.
  restore_status?: string // Recorded restore status.
  restore_error?: string // Recorded restore error.
  restored_at?: string // Recorded restored at.
}

/**
 * BackupPart identifies a stored backup segment with its size and optional
 * integrity digest.
 */
export interface BackupPart {
  index: number // One-based archive part number.
  storage_key?: string // Recorded storage key.
  s3_key?: string // Recorded s3 key.
  size_bytes: number // Content size bytes.
  sha256?: string // SHA-256 content digest.
}

/**
 * BackupDownloadPart pairs one backup segment's index and size with its
 * downloadable URL.
 */
export interface BackupDownloadPart {
  index: number // One-based download part number.
  size_bytes: number // Content size bytes.
  url: string // Resource access URL.
}

/**
 * BackupDownloadResponse exposes a direct download URL or multipart download
 * entries returned for a backup.
 */
export interface BackupDownloadResponse {
  url?: string // Resource access URL.
  parts?: BackupDownloadPart[] // Backup segment metadata.
}

/**
 * CreateBackupRequest optionally selects the expiration period for a newly
 * requested backup.
 */
export interface CreateBackupRequest {
  expire_days?: number // Requested expire days.
}

/**
 * TestS3Response reports whether the backup storage connection test succeeded
 * and supplies its message.
 */
export interface TestS3Response {
  ok: boolean // Connection test success.
  message: string // Server-provided result message.
}

/**
 * getStorageConfig retrieves storage config and returns the server response
 * data.
 */
export async function getStorageConfig(): Promise<BackupStorageConfig> {
  const { data } = await apiClient.get<BackupStorageConfig>('/admin/backups/storage-config')
  return data
}

/**
 * updateStorageConfig updates storage config and returns the server response
 * data.
 */
export async function updateStorageConfig(
  config: BackupStorageConfig,
): Promise<BackupStorageConfig> {
  const { data } = await apiClient.put<BackupStorageConfig>('/admin/backups/storage-config', config)
  return data
}

/**
 * testStorageConnection submits storage connection and returns the server
 * response data.
 */
export async function testStorageConnection(config: BackupStorageConfig): Promise<TestS3Response> {
  const { data } = await apiClient.post<TestS3Response>(
    '/admin/backups/storage-config/test',
    config,
  )
  return data
}

/**
 * getContentConfig retrieves content config and returns the server response
 * data.
 */
export async function getContentConfig(): Promise<BackupContentConfig> {
  const { data } = await apiClient.get<BackupContentConfig>('/admin/backups/content-config')
  return data
}

/**
 * updateContentConfig updates content config and returns the server response
 * data.
 */
export async function updateContentConfig(
  config: BackupContentConfig,
): Promise<BackupContentConfig> {
  const { data } = await apiClient.put<BackupContentConfig>('/admin/backups/content-config', config)
  return data
}

/**
 * getSchedule retrieves schedule and returns the server response data.
 */
export async function getSchedule(): Promise<BackupScheduleConfig> {
  const { data } = await apiClient.get<BackupScheduleConfig>('/admin/backups/schedule')
  return data
}

/**
 * updateSchedule updates schedule and returns the server response data.
 */
export async function updateSchedule(config: BackupScheduleConfig): Promise<BackupScheduleConfig> {
  const { data } = await apiClient.put<BackupScheduleConfig>('/admin/backups/schedule', config)
  return data
}

/**
 * createBackup submits backup and returns the server response data.
 */
export async function createBackup(req?: CreateBackupRequest): Promise<BackupRecord> {
  const { data } = await apiClient.post<BackupRecord>('/admin/backups', req || {})
  return data
}

/**
 * listBackups retrieves backups and returns the server response data.
 */
export async function listBackups(): Promise<{
  items: BackupRecord[] // Returned collection entries.
}> {
  const { data } = await apiClient.get<{
    items: BackupRecord[] // Returned collection entries.
  }>('/admin/backups')
  return data
}

/**
 * getBackup retrieves backup and returns the server response data.
 */
export async function getBackup(id: string): Promise<BackupRecord> {
  const { data } = await apiClient.get<BackupRecord>(`/admin/backups/${id}`)
  return data
}

/**
 * deleteBackup removes backup and returns after the request completes.
 */
export async function deleteBackup(id: string): Promise<void> {
  await apiClient.delete(`/admin/backups/${id}`)
}

/**
 * getDownloadURL retrieves download url and returns the server response data.
 */
export async function getDownloadURL(id: string): Promise<BackupDownloadResponse> {
  const { data } = await apiClient.get<BackupDownloadResponse>(`/admin/backups/${id}/download-url`)
  return data
}

/**
 * downloadBackupFile retrieves the selected backup as binary data. An existing
 * Blob is returned directly; otherwise the received BlobPart is wrapped as an
 * application/gzip Blob for browser download handling.
 */
export async function downloadBackupFile(id: string): Promise<Blob> {
  const { data } = await apiClient.get<BlobPart>(`/admin/backups/${id}/download`, {
    responseType: 'blob',
  })
  /**
   * Some adapters return a BlobPart even for binary responses; wrap that value
   * so download callers consistently receive a gzip Blob.
   */
  return data instanceof Blob ? data : new Blob([data], { type: 'application/gzip' })
}

/**
 * restoreBackup submits restore backup and returns the server response data.
 */
export async function restoreBackup(id: string, password: string): Promise<BackupRecord> {
  const { data } = await apiClient.post<BackupRecord>(`/admin/backups/${id}/restore`, { password })
  return data
}

/**
 * backupAPI groups backup storage, schedules and restore operations for callers
 * using the module's named or default API object.
 */
export const backupAPI = {
  getStorageConfig,
  updateStorageConfig,
  testStorageConnection,
  getContentConfig,
  updateContentConfig,
  getSchedule,
  updateSchedule,
  createBackup,
  listBackups,
  getBackup,
  deleteBackup,
  getDownloadURL,
  downloadBackupFile,
  restoreBackup,
}

export default backupAPI
