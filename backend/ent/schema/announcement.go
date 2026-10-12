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

	"github.com/TokenFlux/TokenRouter/internal/site"
)

// Announcement holds the schema definition for the Announcement entity.
//
// Announcements are hard-deleted; foreign-key cascades remove their read receipts.
type Announcement struct {
	ent.Schema
}

func (Announcement) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "announcements"},
	}
}

func (Announcement) Fields() []ent.Field {
	return []ent.Field{
		field.JSON("localization", site.AnnouncementLocalization{}).Optional().SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.String("title").
			MaxLen(200).
			NotEmpty().
			Comment("Announcement title"),
		field.String("content").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			NotEmpty().
			Comment("Announcement content, with Markdown support"),
		field.String("status").
			MaxLen(20).
			Default(site.AnnouncementStatusDraft).
			Comment("Status: draft, active, archived"),
		field.String("notify_mode").
			MaxLen(20).
			Default(site.AnnouncementNotifyModeSilent).
			Comment("Notification mode: silent (bell only) or popup"),
		field.JSON("targeting", site.AnnouncementTargeting{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("Display conditions as JSON rules"),
		field.Time("starts_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}).
			Comment("Display start time; null takes effect immediately"),
		field.Time("ends_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}).
			Comment("Display end time; null means no end date"),
		field.Int64("created_by").
			Optional().
			Nillable().
			Comment("User ID of the creating administrator"),
		field.Int64("updated_by").
			Optional().
			Nillable().
			Comment("User ID of the updating administrator"),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (Announcement) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("reads", AnnouncementRead.Type),
	}
}

func (Announcement) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status"),
		index.Fields("created_at"),
		index.Fields("starts_at"),
		index.Fields("ends_at"),
	}
}
