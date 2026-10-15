package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// announcement_read.go defines one persisted read receipt per announcement and
// user. The site repository inserts receipts with conflict handling on the
// announcement and user pair, so subsequent reads retain the first recorded
// read time.

// AnnouncementRead records a user's first read of one announcement.
type AnnouncementRead struct {
	ent.Schema // generated entity definition
}

// Annotations maps generated read-receipt operations to the announcement_reads table.
func (AnnouncementRead) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "announcement_reads"},
	}
}

// Fields defines the announcement and reader identities with receipt timestamps.
// Creation supplies current timestamps when omitted; MarkRead can provide the
// read time observed by the site service.
func (AnnouncementRead) Fields() []ent.Field {
	return []ent.Field{
		// Read announcement identity.
		field.Int64("announcement_id"),
		// Reading user identity.
		field.Int64("user_id"),
		// First observed read.
		field.Time("read_at").
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}).
			Comment("Time the user first read the announcement"),
		// Immutable insertion timestamp.
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Edges binds each receipt to its required announcement and user records.
// The scalar identity fields back the unique edges so generated mutations can
// assign those existing entities directly.
func (AnnouncementRead) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("announcement", Announcement.Type).
			Ref("reads").
			Field("announcement_id").
			Unique().
			Required(),
		edge.From("user", User.Type).
			Ref("announcement_reads").
			Field("user_id").
			Unique().
			Required(),
	}
}

// Indexes supports receipt lookup and enforces one receipt per announcement-user
// pair. MarkRead uses the pair's unique index as its conflict target to retain
// the first receipt on repeated reads.
func (AnnouncementRead) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("announcement_id"),
		index.Fields("user_id"),
		index.Fields("read_at"),
		index.Fields("announcement_id", "user_id").Unique(),
	}
}
