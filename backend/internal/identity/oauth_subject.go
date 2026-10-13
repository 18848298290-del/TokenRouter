package identity

import (
	"strings"
)

// LinuxDo profiles may lack an email while still supplying a stable subject.
// Synthetic addresses place that subject after a linuxdo- prefix, and the
// provider checks its ASCII alphabet and byte length before storing the result
// as a local account's reserved email.
//
// The length bound leaves room for the prefix within an email's local part.
// OAuthLinuxDoSyntheticEmail performs the construction and trims the subject,
// but it does not enforce that bound or alphabet itself. Consumers accepting
// external subjects therefore run OAuthIsSafeLinuxDoSubject first, while profile
// candidate selection uses the first nonempty trimmed string from its ordered
// inputs.

// OAuthLinuxDoMaxSubjectLength bounds subject bytes so the linuxdo- prefix and
// accepted subject fit within the synthetic address's 64-byte local part.
const OAuthLinuxDoMaxSubjectLength = 64 - len("linuxdo-")

// OAuthFirstNonEmpty returns the first nonempty trimmed string in input order.
// Whitespace-only candidates are skipped and an empty result means every
// candidate was empty after trimming; it applies no other normalization.
func OAuthFirstNonEmpty(values ...string) string {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return ""
}

// OAuthIsSafeLinuxDoSubject reports whether a trimmed subject fits a synthetic
// LinuxDo address. It requires a nonempty value within the byte bound and accepts
// ASCII letters, digits, underscores and hyphens.
func OAuthIsSafeLinuxDoSubject(subject string) bool {
	subject = strings.TrimSpace(subject)
	if subject == "" || len(subject) > OAuthLinuxDoMaxSubjectLength {
		return false
	}
	for _, r := range subject {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// OAuthLinuxDoSyntheticEmail builds the reserved address for a trimmed subject.
// Empty subjects return an empty string; callers handling external input first
// check OAuthIsSafeLinuxDoSubject because construction itself accepts any
// nonempty text.
func OAuthLinuxDoSyntheticEmail(subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return ""
	}
	return "linuxdo-" + subject + LinuxDoConnectSyntheticEmailDomain
}
