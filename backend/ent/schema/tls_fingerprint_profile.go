package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"

	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
)

// A ClientHello template records the ordered TLS parameters used for an
// outbound connection. Stored profiles let egress construct a fingerprint for
// providers or router selections, using cipher suites, groups, and extensions
// supplied by administrators.

// TLSFingerprintProfile stores the configurable parameters of a TLS ClientHello.
type TLSFingerprintProfile struct {
	ent.Schema // generated entity definition
}

// Annotations maps profile queries and mutations to the tls_fingerprint_profiles table.
func (TLSFingerprintProfile) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "tls_fingerprint_profiles"},
	}
}

// Mixin adds creation and update timestamps to stored TLS profiles.
func (TLSFingerprintProfile) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

// Fields describes a named TLS profile and its ClientHello parameter lists.
// Lists use PostgreSQL JSONB columns and may be omitted; GREASE is disabled
// until enabled for the profile.
func (TLSFingerprintProfile) Fields() []ent.Field {
	return []ent.Field{
		// Unique profile name.
		field.String("name").
			MaxLen(100).
			NotEmpty().
			Unique(),

		// Administrator profile description.
		field.Text("description").
			Optional().
			Nillable(),

		// GREASE extension preference.
		field.Bool("enable_grease").
			Default(false),

		// Cipher suite order affects the JA3 fingerprint.
		field.JSON("cipher_suites", []uint16{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// Supported elliptic curves and groups.
		field.JSON("curves", []uint16{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// EC point formats.
		field.JSON("point_formats", []uint16{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// Signature algorithm identifiers.
		field.JSON("signature_algorithms", []uint16{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// ALPN protocols, such as ["http/1.1"].
		field.JSON("alpn_protocols", []string{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// TLS versions, such as [0x0304, 0x0303].
		field.JSON("supported_versions", []uint16{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// Groups sent in Key Share; 29 is X25519.
		field.JSON("key_share_groups", []uint16{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// PSK key exchange modes; 1 is psk_dhe_ke.
		field.JSON("psk_modes", []uint16{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),

		// TLS extension type IDs in wire order.
		field.JSON("extensions", []uint16{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
	}
}
