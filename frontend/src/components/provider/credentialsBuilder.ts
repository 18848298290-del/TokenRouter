// credentialsBuilder.ts applies optional warmup and Antigravity project
// settings to provider credential drafts. Create and edit callers share
// these mutations while retaining different behavior for an empty setting:
// editing deletes the corresponding field, and creation leaves it untouched.

// applyInterceptWarmup mutates the supplied credential draft according to
// the warmup toggle. Enabled drafts set the flag; disabled edit drafts
// delete it, while disabled create drafts leave existing fields untouched.
export function applyInterceptWarmup(
  credentials: Record<string, unknown>,
  enabled: boolean,
  mode: 'create' | 'edit',
): void {
  if (enabled) {
    credentials.intercept_warmup_requests = true
  } else if (mode === 'edit') {
    delete credentials.intercept_warmup_requests
  }
}

// ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY identifies the project value stored
// in Antigravity credentials by provider forms.
export const ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY = 'antigravity_project_id'

// applyAntigravityProjectID trims and writes a project identifier into the
// supplied credential draft. Empty edit values delete the field; empty
// create values leave the object untouched.
export function applyAntigravityProjectID(
  credentials: Record<string, unknown>,
  projectId: string,
  mode: 'create' | 'edit',
): void {
  const trimmed = projectId.trim()
  if (trimmed) {
    credentials[ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY] = trimmed
  } else if (mode === 'edit') {
    delete credentials[ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY]
  }
}
