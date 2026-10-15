package postgres

import (
	"context"
	"slices"
	"strings"

	"github.com/lib/pq"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// localized_text.go builds SQL expressions that select current translations in
// locale preference order.
// User-facing SQL queries can select translated JSON text while retaining the
// ordinary business field when localized content is unconfigured. Language
// candidates are nested in preference order, and a translation's saved source
// revision has to match the content's source revision before its value
// participates.
//
// A candidate matching the source locale selects source text directly. A
// current translation with a missing value also falls back to source or the
// ordinary expression at that candidate, so later candidates are not
// consulted on that branch. Column parts and JSON field names are quoted for
// SQL, while the caller supplies the trusted original expression.

// LocalizedTextExpression returns SQL selecting localized column content for
// a user-presentation context, or original unchanged otherwise. column is a
// trusted dot-separated identifier and original is a trusted SQL expression;
// language candidates and field names are quoted as literals.
//
// Candidate preference is preserved by nesting CASE expressions in reverse
// order. A source-locale match or matching translation revision chooses that
// candidate's value with source/original fallback. A missing current value
// therefore ends candidate selection there. Nonpositive content revision
// chooses original.
func LocalizedTextExpression(ctx context.Context, column, field, original string) string {
	if !locale.UserPresentation(ctx) {
		return original
	}
	parts := strings.Split(column, ".")
	for i := range parts {
		parts[i] = pq.QuoteIdentifier(parts[i])
	}
	content := strings.Join(parts, ".")
	source := content + "->>'source'"
	if field != "" {
		source = content + "->'source'->>" + pq.QuoteLiteral(field)
	}
	fallback := "COALESCE(" + source + ", " + original + ")"
	expression := fallback
	candidates := locale.Candidates(locale.FromContext(ctx))
	// Build from the least-preferred candidate so the first candidate becomes
	// the outer CASE and decides before later candidates when its revision matches.
	for _, candidate := range slices.Backward(candidates) {
		language := pq.QuoteLiteral(candidate)
		translation := content + "->'translations'->" + language
		value := translation + "->>'value'"
		if field != "" {
			value = translation + "->'value'->>" + pq.QuoteLiteral(field)
		}
		expression = "CASE WHEN " + content + "->>'source_locale'=" + language + " THEN " + fallback +
			" WHEN " + translation + "->>'source_revision'=" + content + "->>'source_revision' THEN COALESCE(" + value + ", " + fallback + ") ELSE " + expression + " END"
	}
	// A zero or absent content revision means localization has not been configured;
	// keep the caller's ordinary business expression available for those rows.
	return "CASE WHEN COALESCE((" + content + "->>'revision')::bigint, 0) > 0 THEN " + expression + " ELSE " + original + " END"
}
