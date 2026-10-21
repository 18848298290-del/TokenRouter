import { apiClient } from './client'
import type {
  TotpStatus,
  TotpSetupRequest,
  TotpSetupResponse,
  TotpEnableRequest,
  TotpEnableResponse,
  TotpDisableRequest,
  TotpVerificationMethod,
} from '@/api/totp.types'

/**
 * totp.ts sends account TOTP enrollment, disabling and step-up requests.
 * Enrollment separates proof preparation from code verification, and
 * verification-method lookup tells callers which proof to collect; step-up
 * returns the session's temporary sensitive-operation authorization result.
 */

/**
 * getStatus retrieves TOTP status for current user and returns the server
 * result.
 */
export async function getStatus(): Promise<TotpStatus> {
  const { data } = await apiClient.get<TotpStatus>('/user/totp/status')
  return data
}

/**
 * getVerificationMethod retrieves verification method for TOTP operations and
 * returns the server result.
 */
export async function getVerificationMethod(): Promise<TotpVerificationMethod> {
  const { data } = await apiClient.get<TotpVerificationMethod>('/user/totp/verification-method')
  return data
}

/**
 * sendVerifyCode sends email verification code for TOTP operations.
 */
export async function sendVerifyCode(): Promise<{
  success: boolean // Operation success flag.
}> {
  const { data } = await apiClient.post<{
    success: boolean // Operation success flag.
  }>('/user/totp/send-code')
  return data
}

/**
 * initiateSetup starts TOTP setup - generates secret and QR code.
 */
export async function initiateSetup(request?: TotpSetupRequest): Promise<TotpSetupResponse> {
  const { data } = await apiClient.post<TotpSetupResponse>('/user/totp/setup', request || {})
  return data
}

/**
 * enable completes TOTP setup by verifying the code.
 */
export async function enable(request: TotpEnableRequest): Promise<TotpEnableResponse> {
  const { data } = await apiClient.post<TotpEnableResponse>('/user/totp/enable', request)
  return data
}

/**
 * disable disables TOTP for current user and returns the server result.
 */
export async function disable(request: TotpDisableRequest): Promise<{
  success: boolean // Operation success flag.
}> {
  const { data } = await apiClient.post<{
    success: boolean // Operation success flag.
  }>('/user/totp/disable', request)
  return data
}

/**
 * TotpStepUpResponse reports successful step-up verification and the temporary
 * authorization lifetime in seconds.
 */
export interface TotpStepUpResponse {
  verified: boolean // Successful verification flag.
  expires_in: number // Lifetime in seconds.
}

/**
 * stepUp verifies a TOTP code to authorize sensitive operations for the current
 * session. The server returns the temporary authorization result; callers supply
 * the six-digit code entered by the user.
 */
export async function stepUp(code: string): Promise<TotpStepUpResponse> {
  const { data } = await apiClient.post<TotpStepUpResponse>('/user/totp/step-up', { code })
  return data
}

/**
 * totpAPI groups TOTP enrollment, verification and step-up requests for callers
 * using the module's named or default API object.
 */
export const totpAPI = {
  getStatus,
  getVerificationMethod,
  sendVerifyCode,
  initiateSetup,
  enable,
  disable,
  stepUp,
}

export default totpAPI
