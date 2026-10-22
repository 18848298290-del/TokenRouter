package provider

import (
	"context"
	"fmt"
)

// credential_version.go captures and compares provider authentication versions
// for conditional credential writes.
// Credential changes need the complete observed authentication identity so a
// late exchange cannot overwrite reauthorization or a status update.
// CredentialVersion carries the expected provider ID, platform, type, status,
// proxy and credential object; CredentialMutationSnapshot also retains token
// details for Grok request recovery.
//
// Comparison treats nil credentials as an empty JSON object and rejects an
// identity that cannot be encoded. ObserveCredentialVersion captures a record
// with copied proxy and supported credential values; CloneCredentialVersion
// copies an existing version before administrative work waits for storage
// locks. Callers still keep unsupported mutable credential values stable, and
// diagnostic formatting hides authentication contents.

// CredentialMutationSnapshot retains captured Grok authentication details for
// conditional request-side recovery writes.
type CredentialMutationSnapshot struct {
	// CredentialsJSON stores the marshaled object for recovery comparison; Grok
	// capture uses literal null when the map is nil or encoding fails.
	CredentialsJSON string `json:"-"`
	AccessToken     string `json:"-"` // trimmed captured access token
	RefreshToken    string `json:"-"` // trimmed captured refresh token
	TokenVersion    int64  `json:"-"` // captured token version stamp
	// ProxyID holds a detached captured proxy value; nil represents a provider
	// observed without a selected proxy at credential capture time.
	ProxyID *int64 `json:"-"`
}

// CredentialVersion carries the observed provider identity compared before
// applying an authentication update.
type CredentialVersion struct {
	// ID, Platform, Type, Status, ProxyID and Credentials describe one observed
	// row. Conditional credential writers compare all six values before applying
	// an exchange so a concurrent administrator update takes precedence.
	ID       int64  // observed provider row
	Platform string // observed upstream platform
	Type     string // observed credential kind
	Status   string // observed persistent health status
	// ProxyID and Credentials can borrow the observation's mutable values.
	// Callers hold them stable throughout comparison. ObserveCredentialVersion
	// and CloneCredentialVersion detach the proxy and supported credential
	// containers when capturing a record or copying an existing version.
	ProxyID     *int64         // observed proxy selection
	Credentials map[string]any `json:"-"` // complete observed credential object
}

// CredentialRefreshWriter conditionally saves an exchange against the observed
// provider authentication state.
type CredentialRefreshWriter interface {
	// UpdateOAuthCredentialsIfUnchanged saves credentials when the expected
	// provider identity still matches the persisted row. False reports a conflict;
	// refresh callers reread current state and discard this exchange result.
	//
	// Production storage locks the matching row in its Ent transaction before
	// credential and outbox writes. A caller-supplied transaction leaves commit
	// and publication with that outer owner, so true then reports applied
	// transaction work rather than an independently committed credential write.
	UpdateOAuthCredentialsIfUnchanged(context.Context, CredentialVersion, map[string]any) (bool, error)
}

// String returns a fixed redacted label for the captured credential details.
func (CredentialMutationSnapshot) String() string { return "CredentialMutationSnapshot{redacted}" }

// GoString uses the redacted String representation for Go-syntax diagnostic
// formatting.
func (s CredentialMutationSnapshot) GoString() string { return s.String() }

// String reports the provider ID while hiding the expected authentication
// values.
func (v CredentialVersion) String() string {
	return fmt.Sprintf("provider credential version (id=%d)", v.ID)
}

// GoString delegates to String so diagnostic formatting exposes only the
// provider ID.
func (v CredentialVersion) GoString() string { return v.String() }

// ObserveCredentialVersion captures provider identity for a conditional write.
// Nil input returns a zero version; a present record with nil credentials gets
// an empty object. Keep the record stable during capture. The proxy value and
// containers supported by CloneValues are copied, while unsupported mutable
// credential values remain shared and need stable owners during comparison.
func ObserveCredentialVersion(value *Record) CredentialVersion {
	if value == nil {
		return CredentialVersion{}
	}
	credentials := CloneValues(value.Credentials)
	if credentials == nil {
		credentials = map[string]any{}
	}
	return CredentialVersion{
		ID:          value.ID,
		Platform:    value.Platform,
		Type:        value.Type,
		Status:      value.Status,
		Credentials: credentials,
		ProxyID:     clonePointer(value.ProxyID),
	}
}

// CloneCredentialVersion detaches the proxy pointer and credential containers
// supported by CloneValues for a queued comparison. Unsupported credential
// values remain shared and need to stay stable throughout the consumer
// operation.
func CloneCredentialVersion(value CredentialVersion) CredentialVersion {
	value.Credentials = CloneValues(value.Credentials)
	value.ProxyID = clonePointer(value.ProxyID)
	return value
}

// MatchesCredentialVersion checks ID and status, then compares the
// authentication identities derived from platform, type, credentials and proxy.
// Nil credentials encode as an empty object; a missing record or unencodable
// identity fails comparison.
func MatchesCredentialVersion(value *Record, expected CredentialVersion) bool {
	if value == nil || value.ID != expected.ID || value.Status != expected.Status {
		return false
	}
	identity := RefreshCredentialIdentity(value)
	return identity != "" && identity == RefreshCredentialIdentity(&Record{Platform: expected.Platform, Type: expected.Type, Credentials: expected.Credentials, ProxyID: expected.ProxyID})
}
