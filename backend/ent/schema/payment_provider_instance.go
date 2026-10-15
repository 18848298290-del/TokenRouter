package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// payment_provider_instance.go defines payment-channel configuration,
// supported methods, availability, and refund settings. ConfigService writes
// configuration as JSON and can also read legacy encrypted values; suspending
// a channel retains its stored configuration, while permitted deletion removes
// the instance row.

// PaymentProviderInstance stores one administrator-configured payment channel and its options.
type PaymentProviderInstance struct {
	ent.Schema // generated entity definition
}

// Annotations maps configured payment channels to the payment_provider_instances database table.
func (PaymentProviderInstance) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "payment_provider_instances"},
	}
}

// Fields describes provider identity, configuration, methods, limits, and refund
// options. Generated Ent inserts start enabled with refund flags disabled.
// ConfigService applies provider-specific configuration validation and coordinates
// the channel's user-refund setting when it writes these fields.
func (PaymentProviderInstance) Fields() []ent.Field {
	return []ent.Field{
		// Payment adapter key.
		field.String("provider_key").
			MaxLen(30).
			NotEmpty(),
		// Administrator display name.
		field.String("name").
			MaxLen(100).
			Default(""),
		// config contains serialized provider settings. ConfigEncryptConfig writes
		// plaintext JSON despite its method name, while ConfigDecryptConfig accepts
		// that format and legacy AES-GCM ciphertext.
		field.String("config").
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// Comma-separated method names.
		field.String("supported_types").
			MaxLen(200).
			Default(""),
		// enabled controls whether ConfigService advertises the instance among
		// available payment methods. A disabled instance retains its configuration
		// until DeleteProviderInstance permits its removal.
		field.Bool("enabled").
			Default(true),
		// Configured checkout mode.
		field.String("payment_mode").
			MaxLen(20).
			Default(""),
		// Configured channel order.
		field.Int("sort_order").
			Default(0),
		// Serialized method limits.
		field.String("limits").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Default(""),
		// refund_enabled and allow_user_refund distinguish channel refund
		// capability from user permission. ConfigService permits user refunds
		// alongside enabled refunds and clears that permission when refund
		// capability is disabled; order eligibility is checked separately.
		// Channel refund capability.
		field.Bool("refund_enabled").
			Default(false),
		// User refund permission.
		field.Bool("allow_user_refund").
			Default(false),
		// Immutable creation timestamp.
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Latest mutation timestamp.
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Indexes supports provider-adapter lookup and filtering configured channels by availability.
func (PaymentProviderInstance) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("provider_key"),
		index.Fields("enabled"),
	}
}
