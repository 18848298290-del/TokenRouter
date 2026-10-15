package ops

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// historical_ingress_cleanup.go classifies historical ingress rejection
// candidates and optionally deletes matching batches.
// Historical ingress cleanup selects a fixed set of local rejection codes and
// message patterns from stored error observations. The store determines
// candidate eligibility and provides increasing primary-key pages; the
// classifier ignores the saved status number and retains rows whose text does
// not match its listed rules.
//
// A dry run counts matches without deletion. Execute mode deletes one matched
// ID batch at a time and rechecks the time cutoff in storage. Successful prior
// batches remain deleted when a later query fails, while the returned progress
// can omit work from a failing deletion. JSON extraction distinguishes
// top-level string identity from nested numeric codes and uses separately
// stored text when decoding fails.

// HistoricalIngressClassifierVersion supplies the fixed rule-set label hashed
// by the maintenance command for its classifier digest in run diagnostics.
const HistoricalIngressClassifierVersion = "ingress-reject-v1"

// HistoricalIngressCandidate carries saved error text and identity for
// historical ingress classification.
type HistoricalIngressCandidate struct {
	// ID is the historical row identity. CleanupHistoricalIngress uses the final
	// ID in each ordered batch as its next exclusive cursor.
	ID int64
	// StatusCode is supplied from the historical row but is not consulted by
	// historicalIngressRejectReason. Candidate eligibility is restricted by the
	// store query.
	StatusCode int
	// Message and Body provide stored fallback text and captured JSON.
	// parseErrorIdentity prefers decoded top-level or nested message text, and
	// uses Message when JSON decoding fails or no message is present.
	//
	// Message: stored error description text. Body: stored error response JSON.
	Message, Body string
}

// HistoricalIngressStore supplies ordered candidates and per-batch deletion for
// the maintenance cleanup.
type HistoricalIngressStore interface {
	// ListCandidates returns eligible rows after the supplied cursor and before
	// the cutoff, bounded by batch size. Rows need strictly increasing IDs so
	// CleanupHistoricalIngress can advance using the final row, and an empty page
	// ends scanning.
	ListCandidates(context.Context, int64, time.Time, int) ([]HistoricalIngressCandidate, error)
	// DeleteCandidates deletes the selected identities still older than the cutoff
	// and returns its affected-row count. PostgreSQL performs each call
	// independently; callers account for earlier successful batches when a later
	// call fails.
	DeleteCandidates(context.Context, []int64, time.Time) (int64, error)
}

// CleanupHistoricalIngress scans ordered historical candidates and returns
// reason counts, scanned rows, matches, and deletions. A false execute flag is
// a dry run. Callers supply a nonnil store and positive batch size; the cutoff
// and batch size are forwarded unchanged, and the final candidate ID advances
// each page.
//
// Execution deletes matching IDs separately for each batch, with the store
// checking the cutoff again. On query or deletion failure, the reason map is
// nil and numeric progress is returned. Earlier successful deletions remain
// committed, and work from a failing deletion may be absent from deleted
// because its error is returned before accumulation.
func CleanupHistoricalIngress(ctx context.Context, store HistoricalIngressStore, before time.Time, batchSize int, execute bool) (map[string]int64, int64, int64, int64, error) {
	counts := make(map[string]int64)
	var cursor, scanned, matched, deleted int64
	for {
		batch, err := store.ListCandidates(ctx, cursor, before, batchSize)
		if err != nil {
			return nil, scanned, matched, deleted, err
		}
		if len(batch) == 0 {
			break
		}
		// Advance from the final scanned identity, including unmatched rows, so the
		// next page does not repeat candidates that are intentionally retained.
		cursor = batch[len(batch)-1].ID
		ids := make([]int64, 0, len(batch))
		for _, item := range batch {
			scanned++
			if reason, ok := historicalIngressRejectReason(item); ok {
				matched++
				counts[reason]++
				ids = append(ids, item.ID)
			}
		}
		// Dry runs accumulate the same classification counts. Execute mode sends each
		// matched ID batch to storage independently, so later failures cannot undo
		// earlier completed deletions.
		if execute && len(ids) > 0 {
			n, err := store.DeleteCandidates(ctx, ids, before)
			if err != nil {
				return nil, scanned, matched, deleted, err
			}
			deleted += n
		}
	}
	return counts, scanned, matched, deleted, nil
}

// parseErrorIdentity returns the trimmed top-level string code and preferred
// message from captured JSON. A decoding error yields an empty code and the
// supplied stored message, discarding any partially decoded fields.
//
// A nonempty top-level message wins, followed by error.message and then the
// stored message. Nested error.code accepts raw JSON for response formats using
// numeric status codes, but it is not used as the returned identity. Message
// text is returned unchanged; historical matching trims it separately.
func parseErrorIdentity(body, fallbackMessage string) (string, string) {
	var payload struct {
		// Code and Message read top-level response metadata, while Error supplies a
		// nested message for other response shapes. Only the top-level string Code is
		// returned as the error identity.
		Code    string `json:"code"`    // top-level string error code
		Message string `json:"message"` // top-level error description text
		Error   struct {
			// Code accepts arbitrary nested JSON, including numeric status values, so
			// those formats can still expose Error.Message. This nested code is decoded
			// but not classified.
			Code    json.RawMessage `json:"code"`    // nested raw code payload
			Message string          `json:"message"` // nested error description text
		} `json:"error"` // nested error response envelope
	}
	// Malformed JSON or an incompatible typed field can prevent reliable body
	// extraction. Use the separately stored message, leaving code-based
	// classification unavailable for that candidate.
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return "", fallbackMessage
	}
	message := payload.Message
	if message == "" {
		message = payload.Error.Message
	}
	if message == "" {
		message = fallbackMessage
	}
	return strings.TrimSpace(payload.Code), message
}

// historicalIngressRejectReason returns a cleanup reason for the listed local
// ingress rejections. A recognized top-level code selects its reason before
// message comparison, and an absent or unrecognized code allows exact
// stored-message matches or the IP-denial prefix.
//
// Message comparisons are case sensitive after trimming. StatusCode does not
// gate a match, and nested numeric codes contribute no code identity through
// parseErrorIdentity. Unmatched quota, user-not-found, or infrastructure
// wording remains outside this rule set unless its message independently
// matches a listed ingress rejection.
func historicalIngressRejectReason(item HistoricalIngressCandidate) (string, bool) {
	code, message := parseErrorIdentity(item.Body, item.Message)
	switch code {
	case "API_KEY_REQUIRED":
		return "missing_key", true
	case "INVALID_API_KEY":
		return "invalid_key", true
	case "API_KEY_DISABLED":
		return "key_disabled", true
	case "USER_INACTIVE":
		return "user_inactive", true
	case "GROUP_DELETED":
		return "group_deleted", true
	case "GROUP_DISABLED":
		return "group_disabled", true
	case "GROUP_NOT_ALLOWED":
		return "group_forbidden", true
	case "ACCESS_DENIED":
		return "ip_acl_denied", true
	case "api_key_in_query_deprecated":
		return "query_key_deprecated", true
	}

	normalized := strings.TrimSpace(message)
	switch {
	case normalized == "API key is required":
		return "missing_key", true
	case normalized == "Invalid API key":
		return "invalid_key", true
	case normalized == "API key is disabled":
		return "key_disabled", true
	case normalized == "User account is not active":
		return "user_inactive", true
	case normalized == "API Key 所属分组已删除":
		return "group_deleted", true
	case normalized == "API Key 所属分组已停用":
		return "group_disabled", true
	case normalized == "API Key 所属专属分组不再允许当前用户使用":
		return "group_forbidden", true
	case normalized == "API Key is not assigned to any group and cannot be used. Please contact the administrator to assign it to a group.":
		return "group_unassigned", true
	case strings.HasPrefix(normalized, "Access denied. Your IP is "):
		return "ip_acl_denied", true
	case normalized == "Query parameter api_key is deprecated. Use Authorization header or key instead.":
		return "query_key_deprecated", true
	default:
		return "", false
	}
}
