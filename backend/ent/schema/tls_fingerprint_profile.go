package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"

	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"
)

// TLSFingerprintProfile defines a TLS ClientHello template.
//
// Profiles reproduce TLS handshake characteristics of clients such as Claude Code or Node.js.
// Each profile configures cipher suites, curves, extensions, and other ClientHello parameters.
// Providers select a profile through Extra.tls_fingerprint_profile_id.
type TLSFingerprintProfile struct {
	ent.Schema
}

// Annotations sets the database table name.
func (TLSFingerprintProfile) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "tls_fingerprint_profiles"},
	}
}

// Mixin supplies creation and update timestamps.
func (TLSFingerprintProfile) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

// Fields defines the TLS ClientHello parameters.
func (TLSFingerprintProfile) Fields() []ent.Field {
	return []ent.Field{
		// Unique profile name.
		field.String("name").
			MaxLen(100).
			NotEmpty().
			Unique(),

		// Profile description.
		field.Text("description").
			Optional().
			Nillable(),

		// Enables GREASE extensions in the ClientHello.
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

		// Signature algorithms.
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
