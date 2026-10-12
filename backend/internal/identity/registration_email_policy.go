package identity

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/net/publicsuffix"
)

var RegistrationEmailDomainPattern = regexp.MustCompile(
	`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`,
)

// RegistrationEmailDomain returns the registrable domain used for registration quotas.
// For example, abc.com and sub.abc.com share abc.com; unrecognized suffixes keep their domain.
func RegistrationEmailDomain(email string) string {
	_, domain, ok := SplitEmailForPolicy(email)
	if !ok {
		return ""
	}
	return NormalizeRegistrationEmailDomain(domain)
}

// NormalizeRegistrationEmailDomain reduces an email domain to its registrable domain.
func NormalizeRegistrationEmailDomain(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(domain, "@")))
	domain = strings.TrimRight(domain, ".")
	if domain == "" {
		return ""
	}
	registrable, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil {
		return domain
	}
	return registrable
}

// NormalizeRegistrationEmailAddress returns a uniqueness key; callers retain the stored email address.
func NormalizeRegistrationEmailAddress(email string) string {
	local, domain, ok := SplitEmailForPolicy(email)
	if !ok {
		return ""
	}
	domain = strings.TrimRight(domain, ".")
	if domain == "" {
		return ""
	}
	// Keep a leading plus so distinct addresses do not collapse to an empty local part.
	if plusIndex := strings.IndexByte(local, '+'); plusIndex > 0 {
		local = local[:plusIndex]
	}
	// Ignore local-part dots for Gmail domains; other providers may treat dots as significant.
	if domain == "gmail.com" || domain == "googlemail.com" {
		if dotStripped := strings.ReplaceAll(local, ".", ""); dotStripped != "" {
			local = dotStripped
		}
		domain = "gmail.com"
	}
	if local == "" {
		return ""
	}
	return local + "@" + domain
}

// IsRegistrationEmailSuffixAllowed checks whether an email is allowed by suffix whitelist.
// Empty whitelist means allow all.
func IsRegistrationEmailSuffixAllowed(email string, whitelist []string) bool {
	if len(whitelist) == 0 {
		return true
	}
	_, domain, ok := SplitEmailForPolicy(email)
	if !ok {
		return false
	}
	suffix := "@" + domain
	for _, allowed := range whitelist {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if strings.HasPrefix(allowed, "@") && suffix == allowed {
			return true
		}
		if strings.HasPrefix(allowed, "*.") && RegistrationEmailDomainMatchesWildcard(domain, allowed) {
			return true
		}
	}
	return false
}

// IsRegistrationEmailSuffixLimited reports whether an email is outside a nonempty allowlist.
func IsRegistrationEmailSuffixLimited(email string, whitelist []string) bool {
	return len(whitelist) > 0 && !IsRegistrationEmailSuffixAllowed(email, whitelist)
}

// NormalizeRegistrationEmailSuffixWhitelist normalizes and validates suffix whitelist items.
func NormalizeRegistrationEmailSuffixWhitelist(raw []string) ([]string, error) {
	return NormalizeRegistrationEmailSuffixWhitelistWithMode(raw, true)
}

// ParseRegistrationEmailSuffixWhitelist parses persisted JSON into normalized suffixes.
// Invalid entries are ignored to keep old misconfigurations from breaking runtime reads.
func ParseRegistrationEmailSuffixWhitelist(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{}
	}
	var items []string
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return []string{}
	}
	normalized, _ := NormalizeRegistrationEmailSuffixWhitelistWithMode(items, false)
	if len(normalized) == 0 {
		return []string{}
	}
	return normalized
}

func NormalizeRegistrationEmailSuffixWhitelistWithMode(raw []string, strict bool) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		normalized, err := NormalizeRegistrationEmailSuffix(item)
		if err != nil {
			if strict {
				return nil, err
			}
			continue
		}
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}

	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func NormalizeRegistrationEmailSuffix(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "", nil
	}

	if after, ok := strings.CutPrefix(value, "*."); ok {
		domain := after
		if !IsValidRegistrationEmailDomain(domain) {
			return "", fmt.Errorf("invalid email suffix: %q", raw)
		}
		return "*." + domain, nil
	}

	domain := value
	if strings.Contains(value, "@") {
		if !strings.HasPrefix(value, "@") || strings.Count(value, "@") != 1 {
			return "", fmt.Errorf("invalid email suffix: %q", raw)
		}
		domain = strings.TrimPrefix(value, "@")
	}

	if !IsValidRegistrationEmailDomain(domain) {
		return "", fmt.Errorf("invalid email suffix: %q", raw)
	}

	return "@" + domain, nil
}

func IsValidRegistrationEmailDomain(domain string) bool {
	return domain != "" &&
		!strings.Contains(domain, "@") &&
		RegistrationEmailDomainPattern.MatchString(domain)
}

func RegistrationEmailDomainMatchesWildcard(domain string, allowed string) bool {
	base := strings.TrimPrefix(allowed, "*.")
	if !IsValidRegistrationEmailDomain(base) {
		return false
	}
	return domain == base || strings.HasSuffix(domain, "."+base)
}

func SplitEmailForPolicy(raw string) (local string, domain string, ok bool) {
	email := strings.ToLower(strings.TrimSpace(raw))
	local, domain, found := strings.Cut(email, "@")
	if !found || local == "" || domain == "" || strings.Contains(domain, "@") {
		return "", "", false
	}
	domain = strings.TrimRight(domain, ".")
	if domain == "" {
		return "", "", false
	}
	return local, domain, true
}
