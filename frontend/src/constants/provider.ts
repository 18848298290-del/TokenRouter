// provider.ts defines quota modes and grouped cloud-region choices used by provider
// forms. Literal values are sent to backend configuration fields, while display labels
// remain presentation data; the grouping helper converts region groups to disabled
// headings followed by their selectable entries.

// Quota threshold values match the backend balance notification modes: fixed compares a
// quantity, while percentage compares a ratio.
export const QUOTA_THRESHOLD_TYPE_FIXED = 'fixed' as const
export const QUOTA_THRESHOLD_TYPE_PERCENTAGE = 'percentage' as const
// QuotaThresholdType limits quota notification inputs to the two supported threshold
// representations.
export type QuotaThresholdType =
  typeof QUOTA_THRESHOLD_TYPE_FIXED | typeof QUOTA_THRESHOLD_TYPE_PERCENTAGE

// Quota reset values distinguish rolling intervals from fixed schedules when provider
// forms serialize their reset configuration.
export const QUOTA_RESET_MODE_ROLLING = 'rolling' as const
export const QUOTA_RESET_MODE_FIXED = 'fixed' as const
// QuotaResetMode represents the supported rolling or fixed quota reset schedule.
export type QuotaResetMode = typeof QUOTA_RESET_MODE_ROLLING | typeof QUOTA_RESET_MODE_FIXED

// VERTEX_LOCATION_OPTIONS groups service-account Vertex locations by display region
// while retaining the location values sent to provider configuration.
export const VERTEX_LOCATION_OPTIONS = [
  {
    label: 'Common',
    options: [
      { value: 'us-central1', label: 'us-central1 (Iowa)' },
      { value: 'global', label: 'global' },
      { value: 'us', label: 'us' },
      { value: 'eu', label: 'eu' },
    ],
  },
  {
    label: 'United States',
    options: [
      { value: 'us-east1', label: 'us-east1 (South Carolina)' },
      { value: 'us-east4', label: 'us-east4 (Northern Virginia)' },
      { value: 'us-east5', label: 'us-east5 (Columbus)' },
      { value: 'us-south1', label: 'us-south1 (Dallas)' },
      { value: 'us-west1', label: 'us-west1 (Oregon)' },
      { value: 'us-west4', label: 'us-west4 (Las Vegas)' },
    ],
  },
  {
    label: 'Europe',
    options: [
      { value: 'europe-west1', label: 'europe-west1 (Belgium)' },
      { value: 'europe-west2', label: 'europe-west2 (London)' },
      { value: 'europe-west3', label: 'europe-west3 (Frankfurt)' },
      { value: 'europe-west4', label: 'europe-west4 (Netherlands)' },
      { value: 'europe-west6', label: 'europe-west6 (Zurich)' },
      { value: 'europe-west8', label: 'europe-west8 (Milan)' },
      { value: 'europe-west9', label: 'europe-west9 (Paris)' },
    ],
  },
  {
    label: 'Asia Pacific',
    options: [
      { value: 'asia-east1', label: 'asia-east1 (Taiwan)' },
      { value: 'asia-east2', label: 'asia-east2 (Hong Kong)' },
      { value: 'asia-northeast1', label: 'asia-northeast1 (Tokyo)' },
      { value: 'asia-northeast3', label: 'asia-northeast3 (Seoul)' },
      { value: 'asia-south1', label: 'asia-south1 (Mumbai)' },
      { value: 'asia-southeast1', label: 'asia-southeast1 (Singapore)' },
      { value: 'australia-southeast1', label: 'australia-southeast1 (Sydney)' },
    ],
  },
] as const

// BEDROCK_REGION_OPTIONS groups AWS region identifiers for provider form choices while
// preserving their backend-facing values and display order.
export const BEDROCK_REGION_OPTIONS = [
  {
    label: 'US',
    options: [
      { value: 'us-east-1', label: 'us-east-1 (N. Virginia)' },
      { value: 'us-east-2', label: 'us-east-2 (Ohio)' },
      { value: 'us-west-1', label: 'us-west-1 (N. California)' },
      { value: 'us-west-2', label: 'us-west-2 (Oregon)' },
      { value: 'us-gov-east-1', label: 'us-gov-east-1 (GovCloud US-East)' },
      { value: 'us-gov-west-1', label: 'us-gov-west-1 (GovCloud US-West)' },
    ],
  },
  {
    label: 'Europe',
    options: [
      { value: 'eu-west-1', label: 'eu-west-1 (Ireland)' },
      { value: 'eu-west-2', label: 'eu-west-2 (London)' },
      { value: 'eu-west-3', label: 'eu-west-3 (Paris)' },
      { value: 'eu-central-1', label: 'eu-central-1 (Frankfurt)' },
      { value: 'eu-central-2', label: 'eu-central-2 (Zurich)' },
      { value: 'eu-south-1', label: 'eu-south-1 (Milan)' },
      { value: 'eu-south-2', label: 'eu-south-2 (Spain)' },
      { value: 'eu-north-1', label: 'eu-north-1 (Stockholm)' },
    ],
  },
  {
    label: 'Asia Pacific',
    options: [
      { value: 'ap-northeast-1', label: 'ap-northeast-1 (Tokyo)' },
      { value: 'ap-northeast-2', label: 'ap-northeast-2 (Seoul)' },
      { value: 'ap-northeast-3', label: 'ap-northeast-3 (Osaka)' },
      { value: 'ap-south-1', label: 'ap-south-1 (Mumbai)' },
      { value: 'ap-south-2', label: 'ap-south-2 (Hyderabad)' },
      { value: 'ap-southeast-1', label: 'ap-southeast-1 (Singapore)' },
      { value: 'ap-southeast-2', label: 'ap-southeast-2 (Sydney)' },
    ],
  },
  {
    label: 'Canada',
    options: [{ value: 'ca-central-1', label: 'ca-central-1 (Canada)' }],
  },
  {
    label: 'South America',
    options: [{ value: 'sa-east-1', label: 'sa-east-1 (Sao Paulo)' }],
  },
] as const

// ProviderSelectGroup represents a named cloud-region group and its readonly selectable
// entries.
export type ProviderSelectGroup = {
  readonly label: string // Visible group or option label.
  // options retains the supplied readonly region entries, which the flattening helper
  // shares with its result after each generated group heading.
  readonly options: readonly {
    readonly value: string | number // Backend region option value.
    readonly label: string // Visible group or option label.
  }[]
}

// groupedProviderSelectOptions flattens cloud-region groups into disabled headings and
// their original option records. Heading values use the __group__ prefix and label,
// while existing options retain their values and reference identity for Select
// consumers.
export const groupedProviderSelectOptions = (groups: readonly ProviderSelectGroup[]) => {
  return groups.flatMap((group) => [
    {
      value: `__group__${group.label}`,
      label: group.label,
      disabled: true,
      kind: 'group',
    },
    ...group.options,
  ])
}
