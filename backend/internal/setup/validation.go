package setup

import (
	"net/mail"
	"regexp"
)

var (
	hostnamePattern = regexp.MustCompile(`^[a-zA-Z0-9.\-:]+$`)
	dbNamePattern   = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)
	usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
)

// validateHostname checks allowed characters and byte length; colons permit IPv6 input.
func validateHostname(host string) bool {
	return hostnamePattern.MatchString(host) && len(host) <= 253
}

func validateDBName(name string) bool {
	return dbNamePattern.MatchString(name) && len(name) <= 63
}

func validateUsername(name string) bool {
	return usernamePattern.MatchString(name) && len(name) <= 63
}

// validateEmail accepts mail.ParseAddress syntax, including display names, up to 254 bytes.
func validateEmail(email string) bool {
	_, err := mail.ParseAddress(email)
	return err == nil && len(email) <= 254
}

func validatePort(port int) bool {
	return port > 0 && port <= 65535
}

func validateSSLMode(mode string) bool {
	switch mode {
	case "disable", "require", "verify-ca", "verify-full":
		return true
	default:
		return false
	}
}
