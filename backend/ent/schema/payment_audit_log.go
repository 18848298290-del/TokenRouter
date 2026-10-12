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

// Payment actions use audit rows to retain order events and refund recovery
// metadata. The refund writer can revise an existing order-action record and
// store earlier JSON details in its history, so recovery data follows the
// current attempt while retaining prior recorded attempts.

// PaymentAuditLog stores order action details and mutable refund recovery records.
// Audit records are hard-deleted when their rows are removed.
type PaymentAuditLog struct {
	ent.Schema // generated entity definition
}

// Annotations maps payment action metadata to the payment_audit_logs database table.
func (PaymentAuditLog) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "payment_audit_logs"},
	}
}

// Fields describes the local order identity, action, detail, operator, and
// insertion time. Generated Ent inserts default to system attribution and
// empty details; created_at remains immutable when refund recovery details
// are later updated.
func (PaymentAuditLog) Fields() []ent.Field {
	return []ent.Field{
		// Decimal local order ID.
		field.String("order_id").
			MaxLen(64),
		// Recorded action name.
		field.String("action").
			MaxLen(50),
		// detail stores the action-specific text. refundAudit serializes recovery
		// attempts as JSON and carries prior details in history when updating
		// an existing action; its row's creation timestamp remains unchanged.
		field.String("detail").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Default(""),
		// Recorded acting principal.
		field.String("operator").
			MaxLen(100).
			Default("system"),
		// Immutable insertion timestamp.
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Indexes supports retrieving audit and recovery rows by their local order identity.
func (PaymentAuditLog) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("order_id"),
	}
}
