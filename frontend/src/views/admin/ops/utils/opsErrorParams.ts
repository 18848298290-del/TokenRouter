// opsErrorParams.ts builds time filters shared by error lists and detail
// dialogs. A complete custom interval supplies explicit timestamps; incomplete
// custom selections use the supported one-hour preset so the backend never
// receives a custom preset token.

// buildOpsErrorTimeParams selects explicit timestamps for a complete custom
// range. Other ranges pass through as time_range, while a custom range missing
// either bound returns the one-hour preset.
export function buildOpsErrorTimeParams(
  timeRange: string,
  customStartTime?: string | null,
  customEndTime?: string | null,
): Record<string, string> {
  if (timeRange === 'custom' && customStartTime && customEndTime) {
    return { start_time: customStartTime, end_time: customEndTime }
  }

  return { time_range: timeRange === 'custom' ? '1h' : timeRange }
}
