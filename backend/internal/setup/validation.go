package setup

import (
	"net/mail"
	"regexp"
)

// validation.go validates setup connection fields, administrator email, and
// transport selections.
// Setup connection fields are later interpolated into PostgreSQL DSNs and a
// target database creation statement. These validators restrict host and
// identifier characters plus byte lengths before CLI or HTTP probes run, while
// email validation accepts mail.ParseAddress forms including display names.

// These patterns limit setup hosts and PostgreSQL identifiers before their values
// reach connection probes. Host validation is character-based rather than DNS or IP resolution.
var (
	hostnamePattern = regexp.MustCompile(`^[a-zA-Z0-9.\-:]+$`)
	dbNamePattern   = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)
	usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
)

// validateHostname reports whether host uses accepted ASCII characters within
// 253 bytes. Colons permit bare IPv6-like input, but the check validates
// neither actual IP structure nor name resolution and does not trim text.
func validateHostname(host string) bool {
	// Host checks use byte length and allow colons for bare IPv6 input.
	return hostnamePattern.MatchString(host) && len(host) <= 253
}

// validateDBName accepts an ASCII letter followed by letters, digits, or
// underscores through 63 bytes. The target is interpolated into CREATE DATABASE
// by bootstrap, so caller validation limits that identifier text.
func validateDBName(name string) bool {
	// PostgreSQL identifiers use this setup limit before database creation interpolation.
	return dbNamePattern.MatchString(name) && len(name) <= 63
}

// validateUsername accepts nonempty ASCII letters, digits, and underscores
// through 63 bytes. Numeric or underscore-leading names are accepted, while
// spaces and punctuation cannot enter the setup connection username through
// this check.
func validateUsername(name string) bool {
	// Limit unquoted connection usernames to the setup identifier byte range.
	return usernamePattern.MatchString(name) && len(name) <= 63
}

// validateEmail reports whether mail.ParseAddress accepts email and its byte
// length is at most 254. Display names and quoted local parts can be accepted,
// and the original submitted string remains available to setup.
func validateEmail(email string) bool {
	_, err := mail.ParseAddress(email)
	// Count full address text bytes, including any display name, for this limit.
	return err == nil && len(email) <= 254
}

// validatePort reports whether port is a positive TCP port through 65535.
func validatePort(port int) bool {
	// Exclude zero because setup needs a selected connection or listener port.
	return port > 0 && port <= 65535
}

// validateSSLMode reports whether mode is an exact accepted setup libpq TLS
// spelling. It accepts disable, require, verify-ca, and verify-full;
// whitespace, case variants, and prefer remain invalid for this setup
// validator.
func validateSSLMode(mode string) bool {
	switch mode {
	case "disable", "require", "verify-ca", "verify-full":
		return true
	default:
		return false
	}
}
