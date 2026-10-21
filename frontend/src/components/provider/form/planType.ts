// planType.ts reads, displays, and updates provider subscription-plan
// overrides for the editor. Stored values and visible labels can differ:
// chatgptpro and pro both display as Pro, while a saved custom string still
// needs a selectable entry. Credential reads accept strings so invalid data
// cannot become a custom option.
//
// Option construction keeps the current stored value when it shares a preset
// label, then appends an unknown value if no case-insensitive match exists.
// Applying a blank selection deletes the credential field, allowing later
// provider processing to determine its plan. Display formatting never
// rewrites an unknown input.

// PlanTypeOption represents a stored plan value and its displayed selector
// label.
export interface PlanTypeOption {
  value: string // Stored plan value.
  label: string // Visible selector label.
  [key: string]: unknown // Additional Select metadata.
}

// planTypeDisplayLabel returns a friendly label for recognized subscription
// plan values. It compares trimmed lowercase strings, maps chatgptpro to
// Pro, and returns unknown inputs exactly as supplied.
export function planTypeDisplayLabel(value: string): string {
  switch (value.trim().toLowerCase()) {
    case 'plus':
      return 'Plus'
    case 'pro':
    case 'chatgptpro':
      return 'Pro'
    case 'free':
      return 'Free'
    case 'team':
      return 'Team'
    default:
      return value
  }
}

// readPlanType returns a trimmed plan string from provider credentials, or
// an empty string. Missing credentials and non-string values remain empty so
// forms do not retain invalid custom options.
export function readPlanType(credentials: Record<string, unknown> | undefined | null): string {
  const v = credentials?.plan_type
  return typeof v === 'string' ? v.trim() : ''
}

// buildPlanTypeOptions supplies the clear option and subscription presets
// while keeping a saved provider value selectable. A current alias such as
// chatgptpro occupies the matching Pro label with its exact stored value.
// Unknown values are appended after the presets when their case-insensitive
// value is absent.
//
// Keeping the saved value separate from its display label prevents opening
// and saving the editor from replacing a canonical subscription value merely
// because the selector presents a shorter label.
export function buildPlanTypeOptions(current: string, clearLabel: string): PlanTypeOption[] {
  const cur = (current || '').trim()
  const curLabel = cur ? planTypeDisplayLabel(cur) : ''
  const presets: PlanTypeOption[] = [
    { value: 'plus', label: 'Plus' },
    { value: 'pro', label: 'Pro' },
    { value: 'free', label: 'Free' },
  ]
  const opts: PlanTypeOption[] = [{ value: '', label: clearLabel }]
  for (const p of presets) {
    if (cur && p.value !== cur && p.label === curLabel) {
      // Preserve the stored alias here so selecting its friendly label
      // does not rewrite the provider credential.
      opts.push({ value: cur, label: p.label })
    } else {
      opts.push(p)
    }
  }
  if (cur && !opts.some((o) => o.value.toLowerCase() === cur.toLowerCase())) {
    opts.push({ value: cur, label: planTypeDisplayLabel(cur) })
  }
  return opts
}

// applyPlanType writes a trimmed plan value into the supplied credential
// object and returns that same object. Empty values delete the field so
// subsequent provider processing can determine the plan.
export function applyPlanType(
  credentials: Record<string, unknown>,
  planType: string,
): Record<string, unknown> {
  const pt = (planType || '').trim()
  if (pt) {
    credentials.plan_type = pt
  } else {
    delete credentials.plan_type
  }
  return credentials
}
