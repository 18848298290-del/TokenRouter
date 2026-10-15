package provider

import (
	"encoding/json"
	"errors"
	"strings"
)

// grok_credential_snapshot.go attaches the failed Grok credential identity to
// errors for later recovery comparisons.
// Credential recovery can reread a provider after the failed token use. The
// failure therefore carries the credential identity observed for that use so
// gateway recovery can compare it with the current record before applying a
// credential or proxy mutation.
//
// Error wrapping leaves classification able to inspect the cause while the
// snapshot retains value-form token and JSON data. Proxy IDs are copied at
// capture, then borrowed by snapshot extraction. JSON encoding can fail for an
// arbitrary credentials map; the null marker remains in that field rather than
// claiming a complete serialization of unsupported values.

// grokCredentialFailureSnapshotError pairs a failure cause with its observed credential identity.
type grokCredentialFailureSnapshotError struct {
	cause error // underlying credential use failure
	// snapshot is captured when the wrapper is created. Extraction returns its
	// value fields and shares the optional ProxyID pointer for read-only comparison.
	snapshot CredentialMutationSnapshot
}

// Error returns the wrapped credential-use failure's diagnostic text unchanged.
// The wrapper is constructed with its cause, so callers retain the underlying
// message when the comparison snapshot accompanies recovery.
func (e *grokCredentialFailureSnapshotError) Error() string { return e.cause.Error() }

// Unwrap returns the underlying Grok credential-use failure for error-chain
// inspection.
func (e *grokCredentialFailureSnapshotError) Unwrap() error { return e.cause }

// WithGrokCredentialFailureSnapshot attaches the observed Grok OAuth credential
// identity to a failure for conditional recovery. Nil errors, nil records, and
// other provider types pass through unchanged; an existing snapshot wrapper in
// the chain is retained.
//
// The snapshot is captured from this supplied record before later recovery can
// reread the provider. Keeping it with the error allows gateway credential
// classification to compare the state that actually failed. Token strings and
// credential JSON are captured as values, while proxy identity uses a copied
// pointer. This helper preserves the cause in the error chain for ordinary
// classification.
func WithGrokCredentialFailureSnapshot(err error, provider *Record) error {
	if err == nil || provider == nil || !provider.IsGrokOAuth() {
		return err
	}
	if _, ok := errors.AsType[*grokCredentialFailureSnapshotError](err); ok {
		return err
	}
	return &grokCredentialFailureSnapshotError{cause: err, snapshot: GrokCredentialMutationSnapshot(provider)}
}

// GrokCredentialFailureSnapshot finds an observed credential identity in an
// error chain. Missing or typed-nil snapshot wrappers return false; a found
// value borrows its ProxyID pointer from the wrapper and is treated as
// read-only.
func GrokCredentialFailureSnapshot(err error) (CredentialMutationSnapshot, bool) {
	var snapshotErr *grokCredentialFailureSnapshotError
	if !errors.As(err, &snapshotErr) || snapshotErr == nil {
		return CredentialMutationSnapshot{}, false
	}
	return snapshotErr.snapshot, true
}

// GrokCredentialMutationSnapshot captures credential JSON, trimmed token strings,
// token version, and a copied proxy identity for recovery comparisons. Nil records
// produce a zero snapshot. Keep the record and its credential values stable while
// serialization and the separate token reads capture this identity.
//
// JSON encoding failures retain the literal null marker, also produced by a nil
// credential map. Recovery compares that string with another captured string,
// and PostgreSQL writers compare it as JSON; it does not independently identify
// an encoding failure. Token and proxy fields remain available separately, so
// consumers decide how to use that incomplete credential representation.
func GrokCredentialMutationSnapshot(provider *Record) CredentialMutationSnapshot {
	if provider == nil {
		return CredentialMutationSnapshot{}
	}
	// Encoding can fail for arbitrary values. Keep the literal null marker used
	// by recovery comparisons, also produced for a nil map, while capturing
	// token fields separately.
	credentialsJSON := "null"
	if encoded, err := json.Marshal(provider.Credentials); err == nil {
		credentialsJSON = string(encoded)
	}
	snapshot := CredentialMutationSnapshot{
		CredentialsJSON: credentialsJSON,

		AccessToken: strings.TrimSpace(provider.GetGrokAccessToken()),

		RefreshToken: strings.TrimSpace(provider.GetGrokRefreshToken()),

		TokenVersion: provider.GetCredentialAsInt64("_token_version"),
	}
	if provider.ProxyID != nil {
		proxyID := *provider.ProxyID
		snapshot.ProxyID = &proxyID
	}
	return snapshot
}

// GrokCredentialProxyIDsEqual compares optional proxy IDs by value, keeping
// absence distinct from zero. Two nil pointers are equal, and a single nil
// pointer differs from every populated proxy identity.
func GrokCredentialProxyIDsEqual(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
