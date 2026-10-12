package egress

import (
	"errors"
	"fmt"
	"strings"
)

// Proxy backup records carry credentials and expiration policy across instances
// whose local database IDs differ. Transfer comparison keys join trimmed
// connection values with pipe delimiters; no escaping or hashing separates
// component text, so delimiter-bearing values can collide and the resulting text
// exposes credentials to privileged backup consumers.
//
// Backup references use names resolved against destination records or earlier
// creations in the import batch. New forward references can remain unresolved
// and disable that item's fallback while preserving a diagnostic. Status
// normalization maps disabled and expired records to inactive, preventing
// restored expired records from immediately entering the active-only expiration
// sweep. Validation checks scheme, host, port, and status before administration
// applies its expiration rules.

// TransferProxy carries backup connection credentials, status, and expiration
// policy for destination import matching.
type TransferProxy struct {
	// ProxyKey is used as supplied when nonempty, otherwise import builds it from
	// connection values. Its delimiter-based representation includes credentials
	// and is unsuitable for public diagnostics.
	ProxyKey     string `json:"proxy_key"`               // credential-bearing transfer comparison text
	Name         string `json:"name"`                    // exported administrator proxy name
	Protocol     string `json:"protocol"`                // exported proxy scheme text
	Host         string `json:"host"`                    // exported proxy host text
	Port         int    `json:"port"`                    // exported TCP port number
	Username     string `json:"username,omitempty"`      // exported proxy authentication username
	Password     string `json:"password,omitempty"`      // exported proxy authentication password
	Status       string `json:"status"`                  // exported persisted proxy status
	ExpiresAt    *int64 `json:"expires_at,omitempty"`    // Unix seconds
	FallbackMode string `json:"fallback_mode,omitempty"` // none/direct/proxy selection text
	// BackupProxyName resolves against existing destination proxies and earlier
	// creations in the batch. A new item with an unresolved name disables that
	// fallback and records a per-item error.
	BackupProxyName string `json:"backup_proxy_name,omitempty"` // destination-resolved backup proxy name
	ExpiryWarnDays  int    `json:"expiry_warn_days,omitempty"`  // days
}

// TransferError records one import item's identity and administrative failure
// message for partial results.
type TransferError struct {
	Kind     string `json:"kind"`                // failed transfer item category
	Name     string `json:"name,omitempty"`      // failed item display name
	ProxyKey string `json:"proxy_key,omitempty"` // credential-bearing item comparison key
	Message  string `json:"message"`             // per-item import failure reason
}

// BuildTransferProxyKey joins trimmed protocol, host, username, password, and
// decimal port with pipe delimiters. The result contains credential text and
// requires privileged handling. Components receive no escaping or hashing.
//
// For the same address, username a|b with password c and username a with
// password b|c both end in |a|b|c. Imports using that key can therefore
// associate different supplied credentials with one existing record. Callers
// treat it as the implemented text comparison value, with collision and
// disclosure limits, rather than a unique credential-free identifier.
func BuildTransferProxyKey(protocol, host string, port int, username, password string) string {
	return fmt.Sprintf("%s|%s|%d|%s|%s", strings.TrimSpace(protocol), strings.TrimSpace(host), port, strings.TrimSpace(username), strings.TrimSpace(password))
}

// ValidateTransferProxy checks required scheme/host text, port range, supported
// literal protocol, and normalized optional status. Protocol comparison uses
// its supplied spelling; expiration/fallback relationships are validated later
// by the administration write path.
func ValidateTransferProxy(item TransferProxy) error {
	if strings.TrimSpace(item.Protocol) == "" {
		return errors.New("proxy protocol is required")
	}
	if strings.TrimSpace(item.Host) == "" {
		return errors.New("proxy host is required")
	}
	if item.Port <= 0 || item.Port > 65535 {
		return errors.New("proxy port is invalid")
	}
	switch item.Protocol {
	case "http", "https", "socks5", "socks5h":
	default:
		return fmt.Errorf("proxy protocol is invalid: %s", item.Protocol)
	}
	if item.Status != "" {
		normalizedStatus := NormalizeTransferProxyStatus(item.Status)
		if normalizedStatus != StatusActive && normalizedStatus != "inactive" {
			return fmt.Errorf("proxy status is invalid: %s", item.Status)
		}
	}
	return nil
}

// DefaultTransferProxyName supplies imported-proxy when name contains only
// whitespace. A nonblank name is returned with its supplied surrounding
// whitespace, allowing callers to preserve an explicit backup label.
func DefaultTransferProxyName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "imported-proxy"
	}
	return name
}

// NormalizeTransferProxyStatus trims and lowercases stored status text, mapping
// disabled and expired to inactive for import. Empty remains empty so reused
// items can omit status synchronization, and unknown nonblank values remain
// available for rejection by validation.
func NormalizeTransferProxyStatus(status string) string {
	normalized := strings.TrimSpace(strings.ToLower(status))
	switch normalized {
	case "":
		return ""
	case StatusActive:
		return StatusActive
	case "inactive", "disabled":
		return "inactive"
	case "expired":
		// Import expired records as inactive. The expiry sweep selects active
		// records only, so restored expired items do not immediately trigger provider reassignment.
		return "inactive"
	default:
		return normalized
	}
}
