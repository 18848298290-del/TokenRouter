package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// A failure can be recorded after an administrator has replaced credentials or
// cleared a runtime block. Its notice therefore carries the authentication
// identity captured before persistence, and the observer captures its clear
// generation before that write. Publication can reject a result whose generation
// has become stale.
//
// RefreshCredentialIdentity combines platform, credential kind, the complete
// credential object and proxy selection as JSON before hashing. Nil credentials
// normalize to an empty object so decoded and constructed empty records compare
// alike. Health fields remain separate from this authentication identity,
// allowing failure handling to change status while still describing the
// attempted credentials. Failed JSON encoding returns an empty identity for
// consumers to interpret.

// RefreshFailureNotice describes the attempted authentication identity for
// process-local failure publication.
type RefreshFailureNotice struct {
	// ProviderID, Platform, Type and Identity identify the attempted credentials.
	// RuntimeBlockState accepts notices for its captured provider ID and supported
	// platforms, and an empty Identity cannot install a credential-scoped block.
	ProviderID int64 // attempted provider row
	// Platform: attempted service identity; Type: attempted provider credential kind.
	Platform, Type string
	Identity       string `json:"-"` // attempted authentication identity digest
	// Until is the proposed block deadline; RuntimeBlockState supplies its
	// fallback duration when this value is zero or already expired.
	Until  time.Time
	Reason string // failure publication diagnostic reason
}

// RefreshFailureObserver supplies generation-aware publication after a
// conditional failure write completes.
type RefreshFailureObserver interface {
	// PrepareRefreshFailure captures the selected provider's clear generation and
	// returns a callable publisher. RuntimeBlockState checks that generation again
	// when publishing, so cleanup between preparation and publication protects
	// newer state.
	PrepareRefreshFailure(int64) func(RefreshFailureNotice)
}

// String reports the provider ID while hiding the captured authentication
// digest.
func (n RefreshFailureNotice) String() string {
	return fmt.Sprintf("refresh failure notice (provider_id=%d)", n.ProviderID)
}

// GoString uses String so Go-syntax formatting omits captured authentication
// details.
func (n RefreshFailureNotice) GoString() string { return n.String() }

// PrepareRefreshFailureNotice captures authentication identity and obtains the
// observer's publication callback before a conditional write. Callers invoke the
// returned function after successful persistence, supplying that outcome's
// deadline and reason.
//
// A nil observer or record yields a callable no-op. Otherwise the observer needs
// to return a callable publisher, and the borrowed record must stay stable
// during capture. The returned closure mutates its captured notice, so callers
// serialize its use; the observer decides whether its captured generation still
// permits publication.
func PrepareRefreshFailureNotice(observer RefreshFailureObserver, value *Record) func(time.Time, string) {
	if observer == nil || value == nil {
		return func(time.Time, string) {}
	}
	notice := FailureNotice(value)
	// Capture the observer generation before the caller writes failure state so
	// a recovery clearing that generation can reject a later publication.
	publish := observer.PrepareRefreshFailure(value.ID)
	return func(until time.Time, reason string) {
		notice.Until = until
		notice.Reason = reason
		publish(notice)
	}
}

// RefreshCredentialIdentity returns a hexadecimal SHA-256 digest of platform,
// credential kind, complete credentials and proxy selection. The record and its
// borrowed credential values need to stay stable while JSON is encoded.
//
// Nil credentials are represented as an empty object so equivalent empty
// configurations share an identity. Nil records and encoding failures return an
// empty string, and consumers decide how that result affects admission or
// comparison. Health fields are excluded because failure persistence can change
// them while referring to the same authentication attempt.
func RefreshCredentialIdentity(value *Record) string {
	if value == nil {
		return ""
	}
	credentials := value.Credentials
	if credentials == nil {
		credentials = map[string]any{}
	}
	data, err := json.Marshal(struct {
		// Platform: authentication service identity; Type: stored provider credential kind.
		Platform, Type string
		// Credentials and ProxyID borrow the observed record through marshaling.
		// Their owners hold these values stable until JSON has captured both
		// pieces of the authentication identity.
		Credentials map[string]any // complete borrowed credential object
		ProxyID     *int64         // observed proxy selection
	}{value.Platform, value.Type, credentials, value.ProxyID})
	// An unencodable credential object supplies no comparable digest. Recovery
	// and runtime-block consumers can reject this empty identity before mutation.
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// FailureNotice captures the provider ID, platform, type and computed
// authentication digest for a failure. Nil input returns a zero notice, and the
// borrowed record needs to remain stable during identity encoding.
func FailureNotice(value *Record) RefreshFailureNotice {
	if value == nil {
		return RefreshFailureNotice{}
	}
	return RefreshFailureNotice{ProviderID: value.ID, Platform: value.Platform, Type: value.Type, Identity: RefreshCredentialIdentity(value)}
}
