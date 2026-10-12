package egress

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// Periodic monitoring distinguishes a caller-recognized official host from a
// custom endpoint before enforcing custom-host policy. A parsed official
// hostname is accepted immediately, even when its scheme would fail the later
// custom URL checks. The caller therefore supplies an official recognizer and
// owns any additional requirements for that early acceptance.
//
// Custom hosts require an enabled allowlist policy. They then pass through the
// shared HTTP URL validator, which checks configured scheme permission,
// normalized host patterns, and literal private-address permission. DNS-result
// validation stays with the request executor. Rejections use a monitor-specific
// error so periodic usage scheduling can distinguish a disallowed custom target
// from malformed URL input.

// ErrMonitorURLInvalid identifies monitor URLs that cannot be parsed or contain
// no hostname for target selection.
var ErrMonitorURLInvalid = errors.New("invalid monitor URL")

// MonitorHostPolicy accepts official hosts and requires an enabled allowlist for custom monitor hosts.
type MonitorHostPolicy struct {
	// Enabled activates the configured allowlist for custom hosts; a disabled
	// policy rejects every custom host. AllowInsecureHTTP permits HTTP schemes for
	// custom hosts checked through that allowlist. AllowPrivate permits nonpublic
	// literal addresses in custom URLs, while DNS results remain the executor's
	// responsibility.
	Enabled, AllowInsecureHTTP, AllowPrivate bool
	AllowedHosts                             []string // custom monitor host patterns
}

// Clone copies the configured host-pattern slice into independent storage. Nil
// and present empty slices retain their forms, allowing a consumer to modify
// its host list independently of the source policy.
func (p MonitorHostPolicy) Clone() MonitorHostPolicy {
	p.AllowedHosts = slices.Clone(p.AllowedHosts)
	return p
}

// Validate accepts a parsed hostname recognized by official, or checks a custom
// URL under the enabled allowlist. Parsing failure or an absent hostname returns
// ErrMonitorURLInvalid. Official recognition receives the lowercased hostname
// and returns before scheme or private-address validation.
//
// Custom hosts fail when the policy is disabled. Enabled custom-host checks use
// AllowInsecureHTTP, AllowedHosts, and AllowPrivate through ValidateHTTPURL;
// failures are wrapped in the monitor-specific rejection. Callers impose
// additional official-URL requirements and validate DNS results when executing
// the request.
func (p MonitorHostPolicy) Validate(raw string, official func(string) bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return ErrMonitorURLInvalid
	}
	if official != nil && official(strings.ToLower(parsed.Hostname())) {
		return nil
	}
	if !p.Enabled {
		return errors.New("CN_USAGE_MONITOR_CUSTOM_HOST_NOT_ALLOWLISTED")
	}
	_, err = ValidateHTTPURL(raw, p.AllowInsecureHTTP, ValidationOptions{AllowedHosts: p.AllowedHosts, RequireAllowlist: true, AllowPrivate: p.AllowPrivate})
	if err != nil {
		return fmt.Errorf("CN_USAGE_MONITOR_CUSTOM_HOST_NOT_ALLOWLISTED: %w", err)
	}
	return nil
}
