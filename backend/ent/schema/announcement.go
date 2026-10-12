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

// Announcements store editable content separately from each user's read receipt.
// Publication status and optional display times let the site repository select
// active messages, while creation and update timestamps support administration
// and optimistic checks when an administrator edits a message.

// Announcement describes persisted site messages and their publication settings.
type Announcement struct {
	ent.Schema // generated entity definition
}

// Annotations binds announcement mutations and queries to the announcements table.
func (Announcement) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "announcements"},
	}
}

// Fields describes announcement content, targeting, authors, and display times.
// New records start as silent drafts. Optional start and end times allow the
// site repository to apply open-ended publication windows.
func (Announcement) Fields() []ent.Field {
	return []ent.Field{
		// Translated announcement content.
		field.JSON("localization", site.AnnouncementLocalization{}).Optional().SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		// Default display title.
		field.String("title").
			MaxLen(200).
			NotEmpty().
			Comment("Announcement title"),
		// Default Markdown content.
		field.String("content").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			NotEmpty().
			Comment("Announcement content, with Markdown support"),
		// Stored publication state.
		field.String("status").
			MaxLen(20).
			Default(site.AnnouncementStatusDraft).
			Comment("Status: draft, active, archived"),
		// User notification behavior.
		field.String("notify_mode").
			MaxLen(20).
			Default(site.AnnouncementNotifyModeSilent).
			Comment("Notification mode: silent (bell only) or popup"),
		// Audience selection rules.
		field.JSON("targeting", site.AnnouncementTargeting{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}).
			Comment("Display conditions as JSON rules"),
		// starts_at and ends_at delimit visibility for ListActive. Missing endpoints
		// leave that side of the window open; an existing end time excludes the
		// announcement at that instant.
		// Inclusive display start.
		field.Time("starts_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}).
			Comment("Display start time; null takes effect immediately"),
		// Exclusive display endpoint.
		field.Time("ends_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}).
			Comment("Display end time; null means no end date"),
		// Creating administrator identity.
		field.Int64("created_by").
			Optional().
			Nillable().
			Comment("User ID of the creating administrator"),
		// Updating administrator identity.
		field.Int64("updated_by").
			Optional().
			Nillable().
			Comment("User ID of the updating administrator"),
		// Immutable creation timestamp.
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		// Last mutation timestamp.
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Edges exposes the read receipts belonging to each persisted announcement.
func (Announcement) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("reads", AnnouncementRead.Type),
	}
}

// Indexes supports publication filtering, time-window lookup, and administrative chronology.
func (Announcement) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status"),
		index.Fields("created_at"),
		index.Fields("starts_at"),
		index.Fields("ends_at"),
	}
}
