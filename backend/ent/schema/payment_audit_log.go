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

// PaymentAuditLog holds the schema definition for the PaymentAuditLog entity.
//
// Audit records are hard-deleted.
// Records include payment events and refund recovery state.
// Refund retries can update recovery entries while retaining prior details in their history.
type PaymentAuditLog struct {
	ent.Schema
}

func (PaymentAuditLog) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "payment_audit_logs"},
	}
}

func (PaymentAuditLog) Fields() []ent.Field {
	return []ent.Field{
		field.String("order_id").
			MaxLen(64),
		field.String("action").
			MaxLen(50),
		field.String("detail").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Default(""),
		field.String("operator").
			MaxLen(100).
			Default("system"),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (PaymentAuditLog) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("order_id"),
	}
}
