// dashboard.types.ts defines administrative totals and per-user reporting records.
// Dashboard aggregates distinguish standard charges, actual account deductions, and
// provider costs, while user rankings carry the reporting interval and totals needed to
// interpret the displayed results.

// DashboardStats aggregates account populations, provider health, request throughput, and
// billing totals.
export interface DashboardStats {
  // Account population statistics.
  total_users: number // Registered account count.
  today_new_users: number // Accounts registered today.
  active_users: number // Users with requests today.
  hourly_active_users: number // Active users during the current UTC hour.
  stats_updated_at: string // Statistics update time in UTC RFC3339.
  stats_stale: boolean // Whether statistics are stale.

  // API Key population statistics.
  total_api_keys: number // Total api keys.
  active_api_keys: number // Keys whose status is active.

  // Provider population statistics.
  total_providers: number // Registered provider count.
  normal_providers: number // Providers in normal state.
  error_providers: number // Providers in error state.
  ratelimit_providers: number // Currently rate-limited provider count.
  overload_providers: number // Currently overloaded provider count.

  // Cumulative token usage statistics.
  total_requests: number // Total observed requests.
  total_input_tokens: number // Total input tokens.
  total_output_tokens: number // Total output tokens.
  total_cache_creation_tokens: number // Total cache creation tokens.
  total_cache_read_tokens: number // Total cache read tokens.
  total_tokens: number // Total consumed tokens.
  total_cost: number // Cumulative standard billing amount.
  total_actual_cost: number // Cumulative actual deduction amount.
  total_provider_cost: number // Cumulative provider cost amount.

  // Today's token usage statistics.
  today_requests: number // Today's request count.
  today_input_tokens: number // Today input tokens.
  today_output_tokens: number // Today output tokens.
  today_cache_creation_tokens: number // Today cache creation tokens.
  today_cache_read_tokens: number // Today cache read tokens.
  today_tokens: number // Tokens consumed today.
  today_cost: number // Today's standard billing amount.
  today_actual_cost: number // Today's actual deduction amount.
  today_provider_cost: number // Today's provider cost amount.

  // System runtime statistics.
  average_duration_ms: number // Mean request duration in milliseconds.
  uptime: number // System uptime in seconds.

  // System throughput observations.
  rpm: number // Mean requests per minute over five minutes.
  tpm: number // Mean tokens per minute over five minutes.
}

// UserBreakdownItem summarizes one user's requests, token consumption, and billing
// amounts.
export interface UserBreakdownItem {
  user_id: number // Owning account identifier.
  email: string // Account email address.
  requests: number // Observed request count.
  input_tokens: number // Consumed input tokens.
  output_tokens: number // Produced output tokens.
  cache_tokens: number // Consumed cache tokens.
  total_tokens: number // Total consumed tokens.
  cost: number // Standard billed amount.
  actual_cost: number // Actual charged amount.
  provider_cost: number // Provider billing cost.
}

// UserUsageTrendPoint records one user's request and spending totals for a time bucket.
export interface UserUsageTrendPoint {
  date: string // Reporting calendar date.
  user_id: number // Owning account identifier.
  email: string // Account email address.
  username: string // Account display username.
  requests: number // Observed request count.
  tokens: number // Consumed token count.
  cost: number // Standard request billing amount.
  actual_cost: number // Actual account deduction amount.
}

// UserSpendingRankingItem identifies a ranked user and their actual spending and request
// totals.
export interface UserSpendingRankingItem {
  user_id: number // Owning account identifier.
  email: string // Account email address.
  username: string // Account display username.
  actual_cost: number // Actual charged amount.
  requests: number // Observed request count.
  tokens: number // Consumed token count.
}

// UserSpendingRankingResponse includes ranked users and totals for the requested
// reporting interval.
export interface UserSpendingRankingResponse {
  ranking: UserSpendingRankingItem[] // Users ordered by spending.
  total_actual_cost: number // Total actual cost.
  total_requests: number // Total observed requests.
  total_tokens: number // Total consumed tokens.
  start_date: string // Reporting interval start.
  end_date: string // Reporting interval end.
}
