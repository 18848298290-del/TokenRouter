package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// RefreshFailureNotice identifies a failed credential version; Identity is process-local and omitted from JSON
// and string formatting.
type RefreshFailureNotice struct {
	ProviderID     int64
	Platform, Type string
	Identity       string `json:"-"`
	Until          time.Time
	Reason         string
}

// RefreshFailureObserver captures the block generation before a conditional write and checks it when publishing.
type RefreshFailureObserver interface {
	PrepareRefreshFailure(int64) func(RefreshFailureNotice)
}

func (n RefreshFailureNotice) String() string {
	return fmt.Sprintf("refresh failure notice (provider_id=%d)", n.ProviderID)
}
func (n RefreshFailureNotice) GoString() string { return n.String() }

// PrepareRefreshFailureNotice captures the authentication identity and observer generation before persistence.
func PrepareRefreshFailureNotice(observer RefreshFailureObserver, value *Record) func(time.Time, string) {
	if observer == nil || value == nil {
		return func(time.Time, string) {}
	}
	notice := FailureNotice(value)
	publish := observer.PrepareRefreshFailure(value.ID)
	return func(until time.Time, reason string) {
		notice.Until = until
		notice.Reason = reason
		publish(notice)
	}
}

// RefreshCredentialIdentity hashes platform, type, credentials, and proxy, excluding health fields changed by
// failure handling.
func RefreshCredentialIdentity(value *Record) string {
	if value == nil {
		return ""
	}
	credentials := value.Credentials
	if credentials == nil {
		credentials = map[string]any{}
	}
	data, err := json.Marshal(struct {
		Platform, Type string
		Credentials    map[string]any
		ProxyID        *int64
	}{value.Platform, value.Type, credentials, value.ProxyID})
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func FailureNotice(value *Record) RefreshFailureNotice {
	if value == nil {
		return RefreshFailureNotice{}
	}
	return RefreshFailureNotice{ProviderID: value.ID, Platform: value.Platform, Type: value.Type, Identity: RefreshCredentialIdentity(value)}
}
