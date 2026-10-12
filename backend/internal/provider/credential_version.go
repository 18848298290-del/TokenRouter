package provider

import (
	"context"
	"fmt"
)

// CredentialMutationSnapshot records the identity observed during credential use for conditional writes.
type CredentialMutationSnapshot struct {
	CredentialsJSON string `json:"-"`
	AccessToken     string `json:"-"`
	RefreshToken    string `json:"-"`
	TokenVersion    int64  `json:"-"`
	ProxyID         *int64 `json:"-"`
}

// CredentialVersion contains the identity compared when saving refreshed credentials.
// Compare provider identity, proxy, and all pre-exchange credentials, not just access_token.
type CredentialVersion struct {
	ID          int64
	Platform    string
	Type        string
	Status      string
	ProxyID     *int64
	Credentials map[string]any `json:"-"`
}

// CredentialRefreshWriter saves credentials only while the exchange's input state still matches.
// False reports a state conflict; callers reread instead of exchanging again or overwriting newer values.
type CredentialRefreshWriter interface {
	UpdateOAuthCredentialsIfUnchanged(context.Context, CredentialVersion, map[string]any) (bool, error)
}

func (CredentialMutationSnapshot) String() string { return "CredentialMutationSnapshot{已脱敏}" }

func (s CredentialMutationSnapshot) GoString() string { return s.String() }

func (v CredentialVersion) String() string {
	return fmt.Sprintf("provider credential version (id=%d)", v.ID)
}

func (v CredentialVersion) GoString() string { return v.String() }

// CloneCredentialVersion copies comparison inputs so callers cannot mutate them while waiting for a lock.
func CloneCredentialVersion(value CredentialVersion) CredentialVersion {
	value.Credentials = CloneValues(value.Credentials)
	value.ProxyID = clonePointer(value.ProxyID)
	return value
}

// MatchesCredentialVersion compares CAS identity fields, treating nil credentials as an empty object.
func MatchesCredentialVersion(value *Record, expected CredentialVersion) bool {
	if value == nil || value.ID != expected.ID || value.Status != expected.Status {
		return false
	}
	identity := RefreshCredentialIdentity(value)
	return identity != "" && identity == RefreshCredentialIdentity(&Record{Platform: expected.Platform, Type: expected.Type, Credentials: expected.Credentials, ProxyID: expected.ProxyID})
}
