package postgres

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/ops"
)

// ops_sla_sql.go builds SQL predicates for business-limit exclusions and SLA
// eligibility from status policy and upstream evidence.
// Client-status exclusion depends on upstream evidence as well as a configured
// ignored-status list. An ignored status is classified as a local exclusion
// only when no stored upstream status, nonempty attempt array, or provider
// owner supplies upstream context. A stored business-limit flag still excludes
// a row even when such context is present.
//
// SQL builders use the same local-exclusion decision for complementary
// business-limited and SLA-countable predicates. Nil ignored-status input
// selects the shared 401/403 defaults, while an allocated empty list disables
// that part of exclusion. Column expressions are interpolated from code and
// integer codes become decimal literals. These predicates classify rows; query
// callers separately decide whether final client status must be at least 400,
// allowing recovered provider observations to participate in upstream counts.

// opsUpstreamContextSQL returns a predicate for stored upstream evidence using
// code-owned SQL expressions. A nonnull upstream status, a nonempty JSON array,
// or a case-folded provider owner makes the result true.
//
// Status zero still counts as present when stored nonnull, including provider
// credential observations. SQL NULL and JSON null attempt data become empty
// arrays, and nonarray JSON is treated as absent through a guarded array-length
// expression. Array elements are not validated here; even a null element makes
// its array nonempty. The generated evidence controls local status exclusion
// rather than proving a particular upstream attempt occurred.
func opsUpstreamContextSQL(upstreamStatusExpr, upstreamErrorsExpr, ownerExpr string) string {
	// Guard array-length evaluation with JSON type inspection so scalar or object
	// payloads do not fail this evidence predicate and are treated as absent.
	upstreamErrorsPresentSQL := fmt.Sprintf(`COALESCE(
  CASE
    WHEN jsonb_typeof(COALESCE(NULLIF(%s, 'null'::jsonb), '[]'::jsonb)) = 'array'
      THEN jsonb_array_length(COALESCE(NULLIF(%s, 'null'::jsonb), '[]'::jsonb))
    ELSE 0
  END,
  0
) > 0`, upstreamErrorsExpr, upstreamErrorsExpr)

	return fmt.Sprintf("(%s IS NOT NULL OR %s OR LOWER(COALESCE(%s, '')) = 'provider')", upstreamStatusExpr, upstreamErrorsPresentSQL, ownerExpr)
}

// opsIgnoredStatusCodeSQL builds a status-membership predicate using trusted
// column expression text and normalized integer codes. Nil input selects
// 401/403 defaults, an allocated empty list yields FALSE, and a null status
// compares as zero. It adds no bound parameters.
func opsIgnoredStatusCodeSQL(statusExpr string, ignoredStatusCodes []int) string {
	codes := ops.NormalizeOpsIgnoredStatusCodes(ignoredStatusCodes)
	if len(codes) == 0 {
		return "FALSE"
	}
	// Emit integer codes as decimal literals without adding query parameters,
	// keeping the owning statement's existing placeholder numbering unchanged.
	parts := make([]string, 0, len(codes))
	for _, code := range codes {
		parts = append(parts, strconv.Itoa(code))
	}
	return fmt.Sprintf("COALESCE(%s, 0) IN (%s)", statusExpr, strings.Join(parts, ", "))
}

// opsClientSideStatusExcludedSQL returns the local client-status exclusion
// predicate from code-owned expressions. It combines membership in the
// normalized ignored list with absence of stored upstream context.
//
// A matching status is therefore eligible for SLA when upstream evidence is
// present, unless a separate business-limit flag excludes it elsewhere. Context
// uses nullable status presence, any nonempty attempt array, or provider
// ownership, so status zero and an array containing only null can still
// suppress this local exclusion. Callers retain responsibility for row
// selection and final-status filtering.
func opsClientSideStatusExcludedSQL(statusExpr, upstreamStatusExpr, upstreamErrorsExpr, ownerExpr string, ignoredStatusCodes []int) string {
	// Require the ignored status and absent upstream evidence together, keeping
	// provider-auth zero status or retained attempt arrays from being excluded by
	// client status alone.
	return fmt.Sprintf("(%s AND NOT %s)", opsIgnoredStatusCodeSQL(statusExpr, ignoredStatusCodes), opsUpstreamContextSQL(upstreamStatusExpr, upstreamErrorsExpr, ownerExpr))
}

// opsBusinessLimitedSQL returns a predicate combining the stored business flag
// with local ignored-status exclusion. All expression arguments are trusted SQL
// fragments supplied by query builders.
//
// A true business flag wins even when provider or attempt context prevents
// local status exclusion. A null flag is treated as false, leaving the
// status/context rule to decide the result. The helper classifies the row
// independently of its final status; queryOpsErrorCounts applies its
// client-error cutoff separately, while other readers can use the predicate for
// their own row sets.
func opsBusinessLimitedSQL(statusExpr, upstreamStatusExpr, upstreamErrorsExpr, ownerExpr, businessExpr string, ignoredStatusCodes []int) string {
	return fmt.Sprintf("(COALESCE(%s, false) OR %s)", businessExpr, opsClientSideStatusExcludedSQL(statusExpr, upstreamStatusExpr, upstreamErrorsExpr, ownerExpr, ignoredStatusCodes))
}

// opsSLACountableSQL returns the complementary eligibility predicate for stored
// error observations using trusted SQL expressions. A row qualifies when its
// business flag is false or null and local ignored-status exclusion is false.
//
// Upstream evidence can keep an otherwise ignored client status eligible, but
// cannot override a true stored business flag. This expression imposes no
// final-error status cutoff itself. Callers such as queryOpsErrorCounts apply
// status at least 400 for final-client SLA counts while provider-specific
// counts can also select recovered observations that satisfy this predicate.
func opsSLACountableSQL(statusExpr, upstreamStatusExpr, upstreamErrorsExpr, ownerExpr, businessExpr string, ignoredStatusCodes []int) string {
	return fmt.Sprintf("(NOT COALESCE(%s, false) AND NOT %s)", businessExpr, opsClientSideStatusExcludedSQL(statusExpr, upstreamStatusExpr, upstreamErrorsExpr, ownerExpr, ignoredStatusCodes))
}
