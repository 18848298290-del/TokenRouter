// setupRedirect.ts chooses the destination after installation has already completed. The
// setup guard sends guests to sign-in, authenticated administrators to their management
// dashboard, and other authenticated users to the user dashboard using the two supplied
// session flags.

// resolveCompletedSetupRedirectPath selects a sign-in or dashboard destination from the
// session flags. Unauthenticated callers always receive the sign-in path, while
// authenticated users select their dashboard according to administrator status.
export function resolveCompletedSetupRedirectPath(
  isAuthenticated: boolean,
  isAdmin: boolean,
): string {
  if (!isAuthenticated) {
    return '/login'
  }

  return isAdmin ? '/admin/dashboard' : '/dashboard'
}
