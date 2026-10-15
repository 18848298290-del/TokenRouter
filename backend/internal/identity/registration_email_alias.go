package identity

import (
	"strings"
)

// registration_email_alias.go normalizes mailbox aliases and builds candidate
// probes for account email deduplication.
// Mailbox alias checks compare a normalized identity while retaining the user's
// stored address for login and delivery. Plus suffixes collapse across domains,
// while Gmail and googlemail also share local-part dot removal and one canonical
// domain. Malformed addresses retain their lowercased, trimmed text as the
// identity value rather than producing an empty comparison key.
//
// SQL candidate probes deliberately remove more dots than mailbox normalization,
// including dots in non-Gmail local parts and domain text. The repository then
// rechecks each returned address with NormalizeEmailForAliasDedup, preventing a
// broad candidate match from claiming a different mailbox. Gmail-family input
// produces probes for both saved domain spellings before that final comparison.

// GmailFamilyDomains identifies the lowercase domain spellings whose mailboxes
// share Gmail local-part dot handling and the canonical gmail.com domain value.
var GmailFamilyDomains = map[string]struct{}{
	"gmail.com":      {},
	"googlemail.com": {},
}

// EmailAliasProbe contains broad lookup components for mailbox-alias SQL candidates.
// Local and Domain both omit dots for the repository's SQL expression; candidates
// still need complete alias normalization before they establish mailbox ownership.
type EmailAliasProbe struct {
	Local  string // dot-stripped mailbox local part
	Domain string // dot-stripped candidate domain text
}

// NormalizeEmailForAliasDedup returns the comparison identity used for mailbox
// ownership checks. Callers keep their stored email spelling for account login
// and delivery, since this value combines addresses under the alias policy.
//
// Valid split input loses a plus suffix after the first local-part character.
// Gmail-family domains additionally remove local dots and become gmail.com;
// other domains retain local dots. Unsplit input returns lowercased, trimmed
// text, and an all-dot Gmail local part is kept rather than collapsed to empty.
func NormalizeEmailForAliasDedup(email string) string {
	local, domain, ok := SplitEmailForAliasDedup(email)
	if !ok {
		// Alias lock-key callers retain the trimmed lowercase text even when this
		// input cannot supply the components needed for a SQL candidate probe.
		return strings.ToLower(strings.TrimSpace(email))
	}
	local = StripEmailPlusSuffix(local)
	if IsGmailFamilyDomain(domain) {
		local = StripEmailLocalDots(local)
		domain = "gmail.com"
	}
	return local + "@" + domain
}

// EmailAliasDedupProbes returns broad SQL lookup components for a supplied mailbox.
// Invalid split input or an empty dot-stripped local part returns nil, leaving
// the repository with no candidates to request for that value.
//
// The probe removes all local dots even for non-Gmail addresses because SQL
// performs the same broad removal. Returned rows therefore require a second
// comparison using NormalizeEmailForAliasDedup. Gmail-family input probes both
// gmail.com and googlemail.com, so saved aliases from either domain can reach
// that ownership check.
func EmailAliasDedupProbes(email string) []EmailAliasProbe {
	local, domain, ok := SplitEmailForAliasDedup(email)
	if !ok {
		return nil
	}
	probeLocal := strings.ReplaceAll(StripEmailPlusSuffix(local), ".", "")
	if probeLocal == "" {
		return nil
	}
	domains := []string{domain}
	if IsGmailFamilyDomain(domain) {
		domains = []string{"gmail.com", "googlemail.com"}
	}
	probes := make([]EmailAliasProbe, 0, len(domains))
	for _, candidate := range domains {
		probes = append(probes, EmailAliasProbe{
			Local:  probeLocal,
			Domain: strings.ReplaceAll(candidate, ".", ""),
		})
	}
	return probes
}

// SplitEmailForAliasDedup returns lowercase local and domain components for alias
// handling. It delegates structural splitting to SplitEmailForPolicy, removes
// trailing domain dots and returns false when no usable domain remains.
func SplitEmailForAliasDedup(email string) (local string, domain string, ok bool) {
	local, domain, ok = SplitEmailForPolicy(email)
	if !ok {
		return "", "", false
	}
	domain = strings.TrimRight(domain, ".")
	if domain == "" {
		return "", "", false
	}
	return local, domain, true
}

// StripEmailPlusSuffix removes a tag beginning after the first local character.
// A leading plus remains part of the local value, so distinct addresses such as
// +alice and +bob keep distinct alias identities.
func StripEmailPlusSuffix(local string) string {
	// A leading plus carries the mailbox name here; retaining it prevents
	// different leading-plus addresses from collapsing into the same empty identity.
	if idx := strings.IndexByte(local, '+'); idx > 0 {
		return local[:idx]
	}
	return local
}

// StripEmailLocalDots removes local-part dots when the remaining text is nonempty.
// All-dot input keeps its supplied value, allowing the caller's alias identity
// to retain a local component for that unusual address.
func StripEmailLocalDots(local string) string {
	if stripped := strings.ReplaceAll(local, ".", ""); stripped != "" {
		return stripped
	}
	// All-dot input has no remaining mailbox name after removal, so keep its
	// supplied spelling for the alias comparison instead of returning empty text.
	return local
}

// IsGmailFamilyDomain reports whether the bare domain is in the Gmail family.
// Callers provide lowercase normalized text; the table lookup performs no
// trimming or case conversion before comparing that domain spelling.
func IsGmailFamilyDomain(domain string) bool {
	_, ok := GmailFamilyDomains[domain]
	return ok
}
