package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
)

// Upstream error responses can be selected by status code, body keywords, and
// provider platform. Stored rules decide which upstream details reach clients
// and whether a matched response is included in operations error monitoring.

// ErrorPassthroughRule stores matching conditions and client-facing error settings.
//
// Rules combine the following settings:
//   - Match error codes and keywords.
//   - Pass through the upstream message or return a custom message.
//   - Select the response status code.
//   - Restrict the rule to selected platforms.
type ErrorPassthroughRule struct {
	ent.Schema // generated entity definition
}

// Annotations maps generated error-rule operations to the error_passthrough_rules table.
func (ErrorPassthroughRule) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "error_passthrough_rules"},
	}
}

// Mixin adds automatic creation and update timestamps to error-rule records.
func (ErrorPassthroughRule) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

// Fields defines error matching, response replacement, and monitoring settings.
// Rules default to enabled, any-category matching with upstream status and body
// passthrough; optional values configure narrower matching or replacement content.
func (ErrorPassthroughRule) Fields() []ent.Field {
	return []ent.Field{
		// Rule display name.
		field.String("name").
			MaxLen(100).
			NotEmpty(),

		// Rule activation flag.
		field.Bool("enabled").
			Default(true),

		// priority determines evaluation order in the error-policy service. Lower
		// values are checked first, and the first matching rule sets the response.
		field.Int("priority").
			Default(0),

		// error_codes and keywords form independently optional condition
		// categories. match_mode "any" accepts either configured category;
		// "all" requires every configured category. With neither configured,
		// the error-policy service rejects the rule as a match.
		// Matching upstream statuses.
		field.JSON("error_codes", []int{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// keywords contains alternatives for case-insensitive body matching. The
		// error-policy service searches the first 8 KiB of the upstream body.
		field.JSON("keywords", []string{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// Category combination mode.
		field.String("match_mode").
			MaxLen(10).
			Default("any"),

		// platforms limits evaluation to the listed provider platforms. The
		// error-policy service treats an empty list as applying to every platform.
		field.JSON("platforms", []string{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// passthrough_code and response_code choose the client status together.
		// A true passthrough_code uses the upstream status; false enables the
		// configured response_code replacement for the matched error.
		// Upstream status preference.
		field.Bool("passthrough_code").
			Default(true),

		// Replacement response status.
		field.Int("response_code").
			Optional().
			Nillable(),

		// passthrough_body and custom_message select the client message together.
		// A true passthrough_body uses upstream content; false enables the stored
		// custom_message replacement for the matched error.
		// Upstream message preference.
		field.Bool("passthrough_body").
			Default(true),

		// Replacement error message.
		field.Text("custom_message").
			Optional().
			Nillable(),

		// skip_monitoring suppresses the matched error's operations-log entry.
		// The default false value lets operations monitoring record matching
		// errors along with other upstream failures.
		field.Bool("skip_monitoring").
			Default(false),

		// Administrator rule description.
		field.Text("description").
			Optional().
			Nillable(),
	}
}

// Indexes supports enabled-rule filtering and the configured priority evaluation order.
func (ErrorPassthroughRule) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("enabled"),  // Enabled-rule lookup.
		index.Fields("priority"), // Priority ordering.
	}
}
