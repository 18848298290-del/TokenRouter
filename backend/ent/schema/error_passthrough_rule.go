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

// ErrorPassthroughRule defines how upstream errors are returned to clients.
//
// Rules combine the following settings:
//   - Match error codes and keywords.
//   - Pass through the upstream message or return a custom message.
//   - Select the response status code.
//   - Restrict the rule to selected platforms.
type ErrorPassthroughRule struct {
	ent.Schema
}

// Annotations sets the database table name.
func (ErrorPassthroughRule) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "error_passthrough_rules"},
	}
}

// Mixin supplies creation and update timestamps.
func (ErrorPassthroughRule) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

// Fields defines error matching and response settings.
func (ErrorPassthroughRule) Fields() []ent.Field {
	return []ent.Field{
		// Display name for the rule.
		field.String("name").
			MaxLen(100).
			NotEmpty(),

		// Whether the rule is enabled.
		field.Bool("enabled").
			Default(true),

		// Lower priority values are checked first.
		// The first matching rule determines the response.
		field.Int("priority").
			Default(0),

		// Match any listed error code.
		// For example, [422, 400] matches either code.
		field.JSON("error_codes", []int{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// Match any listed keyword.
		// For example, ["context limit", "model not supported"].
		// Keyword matching is case-insensitive.
		field.JSON("keywords", []string{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// match_mode combines code and keyword matches:
		// - "any": either a code or a keyword matches.
		// - "all": both a code and a keyword match.
		field.String("match_mode").
			MaxLen(10).
			Default("any"),

		// Platforms to which the rule applies.
		// For example, ["anthropic", "openai", "gemini", "antigravity", "qoder", "grok"].
		// An empty list applies to all platforms.
		field.JSON("platforms", []string{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// Selects whether to pass through the upstream status code.
		// true uses the upstream status code.
		// false uses response_code.
		field.Bool("passthrough_code").
			Default(true),

		// Custom response status code.
		// Used when passthrough_code is false.
		field.Int("response_code").
			Optional().
			Nillable(),

		// Selects whether to pass through the upstream error message.
		// true uses the upstream error message.
		// false uses custom_message.
		field.Bool("passthrough_body").
			Default(true),

		// Custom error message.
		// Used when passthrough_body is false.
		field.Text("custom_message").
			Optional().
			Nillable(),

		// Controls whether matched errors are recorded in operations monitoring.
		// true omits matched errors from ops_error_logs.
		// false records them, which is the default.
		field.Bool("skip_monitoring").
			Default(false),

		// Describes the rule's purpose.
		field.Text("description").
			Optional().
			Nillable(),
	}
}

// Indexes supports enabled-rule lookup and priority ordering.
func (ErrorPassthroughRule) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("enabled"),  // Enabled-rule lookup.
		index.Fields("priority"), // Priority ordering.
	}
}
