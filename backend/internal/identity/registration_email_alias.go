package identity

import (
	"strings"
)

// GmailFamilyDomains lists the Gmail domains treated as aliases of the same mailbox.
var GmailFamilyDomains = map[string]struct{}{
	"gmail.com":      {},
	"googlemail.com": {},
}

// EmailAliasProbe contains local and domain parts for SQL candidate lookup.
// Dots are stripped for matching; candidates still require full normalization before comparison.
type EmailAliasProbe struct {
	Local  string
	Domain string
}

// NormalizeEmailForAliasDedup returns a mailbox identity for alias checks.
// Use the stored address for login and delivery.
// All domains lose plus suffixes; Gmail domains also ignore local-part dots and share one domain.
func NormalizeEmailForAliasDedup(email string) string {
	local, domain, ok := SplitEmailForAliasDedup(email)
	if !ok {
		return strings.ToLower(strings.TrimSpace(email))
	}
	local = StripEmailPlusSuffix(local)
	if IsGmailFamilyDomain(domain) {
		local = StripEmailLocalDots(local)
		domain = "gmail.com"
	}
	return local + "@" + domain
}

// EmailAliasDedupProbes returns SQL probes for potential aliases of the supplied mailbox.
// Invalid input or an empty dot-stripped local part returns nil.
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

func StripEmailPlusSuffix(local string) string {
	// Keep a leading plus so +alice and +bob do not collapse to the same empty local part.
	if idx := strings.IndexByte(local, '+'); idx > 0 {
		return local[:idx]
	}
	return local
}

func StripEmailLocalDots(local string) string {
	if stripped := strings.ReplaceAll(local, ".", ""); stripped != "" {
		return stripped
	}
	return local
}

func IsGmailFamilyDomain(domain string) bool {
	_, ok := GmailFamilyDomains[domain]
	return ok
}
