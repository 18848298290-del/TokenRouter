package identity

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// registration_email_policy.go normalizes registration email keys and
// validates or matches domain whitelist entries.
// Registration email policy separates a stored address from the keys used to
// check uniqueness and a domain quota. The uniqueness key folds mailbox aliases;
// the quota key uses the public-suffix library's registrable domain, allowing
// related subdomains to share one registration allowance. Domain derivation
// failures retain the normalized supplied domain for callers to use as a key.
//
// Administrator writes reject invalid whitelist items, while runtime reads of
// stored JSON skip invalid items and return an empty list after unusable input.
// Empty whitelists allow any address at the suffix-check layer, so tolerating a
// wholly invalid saved list has a different effect from rejecting an invalid
// administrator update. Wildcards include their base domain and its subdomains.

// RegistrationEmailDomainPattern accepts lowercase ASCII hostnames with multiple
// labels, bounding each label and rejecting characters outside letters, digits
// and hyphens.
var RegistrationEmailDomainPattern = regexp.MustCompile(
	`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`,
)

// RegistrationEmailDomain returns the registrable domain used for registration
// quota checks. Invalid email splitting returns an empty string; a usable domain
// is reduced with NormalizeRegistrationEmailDomain, including the public-suffix
// library's default rule for unlisted suffixes.
func RegistrationEmailDomain(email string) string {
	_, domain, ok := SplitEmailForPolicy(email)
	if !ok {
		return ""
	}
	return NormalizeRegistrationEmailDomain(domain)
}

// NormalizeRegistrationEmailDomain returns a lowercase registrable-domain key.
// It removes a leading @ before whitespace trimming, removes trailing dots and
// returns empty for an empty normalized value. Public-suffix derivation errors
// return that normalized domain unchanged for quota and lock-key consumers.
func NormalizeRegistrationEmailDomain(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(domain, "@")))
	// The public-suffix parser rejects trailing-dot input, so remove these
	// dots before derivation to give equivalent domain spellings one quota key.
	domain = strings.TrimRight(domain, ".")
	if domain == "" {
		return ""
	}
	registrable, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil {
		// The library cannot derive a registrable domain for this value, so keep
		// the normalized input as the quota identity used by repository callers.
		return domain
	}
	return registrable
}

// NormalizeRegistrationEmailAddress returns the mailbox key used by registration
// uniqueness guards. Callers retain their supplied address for storage and
// delivery, while invalid splitting returns an empty key.
//
// A plus suffix after the first local character is removed for every domain.
// Gmail-family input also folds local dots and googlemail.com into gmail.com.
// Leading plus signs and all-dot local parts retain a mailbox component, while
// non-Gmail local dots stay significant, matching the guarded SQL normalization.
func NormalizeRegistrationEmailAddress(email string) string {
	local, domain, ok := SplitEmailForPolicy(email)
	if !ok {
		return ""
	}
	domain = strings.TrimRight(domain, ".")
	if domain == "" {
		return ""
	}
	// A leading plus is part of the mailbox name in this policy; retaining it
	// keeps separate leading-plus addresses from sharing an empty local identity.
	if plusIndex := strings.IndexByte(local, '+'); plusIndex > 0 {
		local = local[:plusIndex]
	}
	// Gmail and googlemail share a mailbox identity in the registration guard.
	// Folding their local dots and domain spelling lets concurrent alias registrations
	// reach the same uniqueness check while other domains keep local dots.
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

// IsRegistrationEmailSuffixAllowed reports whether the suffix policy accepts an
// address. An empty whitelist accepts any input before parsing; otherwise the
// email must split successfully and match an exact @domain or wildcard entry.
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

// IsRegistrationEmailSuffixLimited reports whether a configured whitelist rejects
// the address. Empty whitelists return false; callers use this distinction to
// decide whether the separate registration-domain quota path needs to apply.
func IsRegistrationEmailSuffixLimited(email string, whitelist []string) bool {
	return len(whitelist) > 0 && !IsRegistrationEmailSuffixAllowed(email, whitelist)
}

// NormalizeRegistrationEmailSuffixWhitelist prepares a whitelist for administrator
// persistence. It uses strict mode, so one invalid nonblank item rejects the
// complete input; normalized duplicates and blank entries are omitted from output.
func NormalizeRegistrationEmailSuffixWhitelist(raw []string) ([]string, error) {
	return NormalizeRegistrationEmailSuffixWhitelistWithMode(raw, true)
}

// ParseRegistrationEmailSuffixWhitelist returns normalized entries from stored
// whitelist JSON. Empty, malformed or wholly unusable input produces an allocated
// empty list, while invalid individual entries are skipped in tolerant mode.
// Consumers treat an empty whitelist as an unrestricted suffix policy.
func ParseRegistrationEmailSuffixWhitelist(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{}
	}
	var items []string
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		// Runtime reads tolerate undecodable saved settings by supplying an empty
		// whitelist; suffix-check consumers then apply their empty-list acceptance rule.
		return []string{}
	}
	normalized, _ := NormalizeRegistrationEmailSuffixWhitelistWithMode(items, false)
	if len(normalized) == 0 {
		return []string{}
	}
	return normalized
}

// NormalizeRegistrationEmailSuffixWhitelistWithMode normalizes whitelist entries
// while choosing how invalid input affects the result. Strict mode returns the
// first validation error with no output; tolerant mode skips invalid entries.
//
// Valid values are deduplicated in their first-occurrence order after case and
// prefix normalization. Blank entries are omitted, and no remaining values
// returns nil. Administrator writes use strict mode to reject a damaged update,
// while stored-value readers use tolerant mode and may receive an empty policy
// when every persisted item is unusable.
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
			// Stored whitelist reads retain valid neighbors after an invalid item;
			// skipping this entry permits runtime parsing to return its usable subset.
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

// NormalizeRegistrationEmailSuffix converts one policy entry to lowercase @domain
// or *.domain form. Empty input returns empty with no error; bare domains receive
// @, and malformed prefixes or unsupported domain syntax return a validation error.
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

// IsValidRegistrationEmailDomain reports whether a domain matches the policy's ASCII hostname syntax.
func IsValidRegistrationEmailDomain(domain string) bool {
	return domain != "" &&
		!strings.Contains(domain, "@") &&
		RegistrationEmailDomainPattern.MatchString(domain)
}

// RegistrationEmailDomainMatchesWildcard reports whether the normalized domain
// equals a wildcard's base or lies beneath it. Invalid base domains return
// false; callers supply lowercase domain text and a prepared whitelist value.
func RegistrationEmailDomainMatchesWildcard(domain string, allowed string) bool {
	base := strings.TrimPrefix(allowed, "*.")
	if !IsValidRegistrationEmailDomain(base) {
		return false
	}
	return domain == base || strings.HasSuffix(domain, "."+base)
}

// SplitEmailForPolicy returns lowercase local and domain parts after trimming
// surrounding whitespace. Empty parts, extra @ characters in the domain or a
// domain made empty by trailing-dot removal return false; this split is not
// complete mailbox-syntax validation.
func SplitEmailForPolicy(raw string) (local string, domain string, ok bool) {
	email := strings.ToLower(strings.TrimSpace(raw))
	local, domain, found := strings.Cut(email, "@")
	if !found || local == "" || domain == "" || strings.Contains(domain, "@") {
		return "", "", false
	}
	// Removing trailing domain dots lets suffix checks and quota lookups match
	// the same stored policy entry when an address supplies either domain spelling.
	domain = strings.TrimRight(domain, ".")
	if domain == "" {
		return "", "", false
	}
	return local, domain, true
}
