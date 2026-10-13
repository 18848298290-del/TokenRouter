package kimi

import "strings"

// Kimi's concurrency-limit classification depends on one fixed message, matched
// after trimming surrounding whitespace. Provider health handling uses this
// predicate for Kimi responses, so changed wording or letter case leaves the
// message outside this classification.

// ConcurrentRequestLimitMessage is the exact message recognized by Kimi's
// concurrency classifier. Provider health handling uses that classification
// when interpreting upstream request failures.
const ConcurrentRequestLimitMessage = "You've reached your concurrent request limit. Please wait for your ongoing requests to finish and try again."

// IsConcurrencyLimitMessage reports whether message matches the configured Kimi
// concurrency-limit text after trimming surrounding whitespace. Matching is
// case-sensitive and requires the complete message, which provider health
// handling uses to distinguish this condition.
func IsConcurrencyLimitMessage(message string) bool {
	return strings.TrimSpace(message) == ConcurrentRequestLimitMessage
}
