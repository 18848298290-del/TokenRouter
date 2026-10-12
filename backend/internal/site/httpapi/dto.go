package httpapi

import (
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
	"github.com/TokenFlux/TokenRouter/internal/site"
)

// Administrator announcement responses include editable localization and targeting
// state, while user responses contain selected copy and read timing. Both
// mappings keep record identities and schedule pointers from their service
// values, so consumers should treat nested data as response input.

// Announcement exposes source content, publication rules, and localization for administrator editing.
type Announcement struct {
	// Localization carries the stored source and translations needed by the
	// administrator editor; the mapper retains its nested collections from the service value.
	Localization site.AnnouncementLocalization `json:"localization"`
	ID           int64                         `json:"id"`          // persistent announcement identifier
	Title        string                        `json:"title"`       // displayed announcement heading
	Content      string                        `json:"content"`     // displayed announcement body
	Status       string                        `json:"status"`      // stored publication state
	NotifyMode   string                        `json:"notify_mode"` // frontend presentation choice

	// Targeting retains the service's group, condition, and plan slices; response
	// consumers need their own nested copies before editing those targeting collections.
	Targeting site.AnnouncementTargeting `json:"targeting"` // eligible reader expression

	// StartsAt and EndsAt are shared service pointers describing a half-open
	// schedule; absent pointers are omitted from JSON and leave that endpoint unbounded.
	StartsAt *time.Time `json:"starts_at,omitempty"` // inclusive start instant
	EndsAt   *time.Time `json:"ends_at,omitempty"`   // exclusive end instant

	// CreatedBy and UpdatedBy retain optional audit pointers from the service
	// record. Rendering reads their identifiers, while modifying the pointed
	// values would also modify that service record's audit fields.
	CreatedBy *int64 `json:"created_by,omitempty"` // creating administrator identifier
	UpdatedBy *int64 `json:"updated_by,omitempty"` // editing administrator identifier

	CreatedAt time.Time `json:"created_at"` // persisted creation timestamp
	UpdatedAt time.Time `json:"updated_at"` // persisted update timestamp
}

// UserAnnouncement exposes selected display copy, schedule, and this user's read timestamp.
type UserAnnouncement struct {
	// Localization is selected-language resolution metadata for the displayed
	// Title and Content, while administrator responses carry the editable content object.
	Localization locale.Resolution `json:"localization"`
	ID           int64             `json:"id"`          // persistent announcement identifier
	Title        string            `json:"title"`       // displayed announcement heading
	Content      string            `json:"content"`     // displayed announcement body
	NotifyMode   string            `json:"notify_mode"` // frontend presentation choice

	// StartsAt and EndsAt retain the service's schedule endpoints for display.
	// A nil pointer is omitted from JSON and indicates no limit on that side.
	StartsAt *time.Time `json:"starts_at,omitempty"` // inclusive start instant
	EndsAt   *time.Time `json:"ends_at,omitempty"`   // exclusive end instant

	// ReadAt retains the service's first-read marker; nil omits the field and
	// identifies an unread announcement to the frontend list and popup selection.
	ReadAt *time.Time `json:"read_at,omitempty"`

	CreatedAt time.Time `json:"created_at"` // persisted creation timestamp
	UpdatedAt time.Time `json:"updated_at"` // persisted update timestamp
}

// AnnouncementFromService maps a service record to its editable administrator response.
// Nil input returns nil. The outer response is new, while localization,
// targeting collections, schedule pointers, and actor pointers are retained
// from the supplied service record.
func AnnouncementFromService(a *site.Announcement) *Announcement {
	if a == nil {
		return nil
	}
	return &Announcement{
		Localization: a.Localization,
		ID:           a.ID,
		Title:        a.Title,
		Content:      a.Content,
		Status:       a.Status,
		NotifyMode:   a.NotifyMode,
		Targeting:    a.Targeting,
		StartsAt:     a.StartsAt,
		EndsAt:       a.EndsAt,
		CreatedBy:    a.CreatedBy,
		UpdatedBy:    a.UpdatedBy,
		CreatedAt:    a.CreatedAt,
		UpdatedAt:    a.UpdatedAt,
	}
}

// UserAnnouncementFromService maps selected announcement copy and user read state to JSON.
// Nil input returns nil. It retains schedule and read-time pointers, and
// maps resolution metadata into the user response's localization field.
func UserAnnouncementFromService(a *site.UserAnnouncement) *UserAnnouncement {
	if a == nil {
		return nil
	}
	return &UserAnnouncement{
		Localization: a.Announcement.Resolution,
		ID:           a.Announcement.ID,
		Title:        a.Announcement.Title,
		Content:      a.Announcement.Content,
		NotifyMode:   a.Announcement.NotifyMode,
		StartsAt:     a.Announcement.StartsAt,
		EndsAt:       a.Announcement.EndsAt,
		ReadAt:       a.ReadAt,
		CreatedAt:    a.Announcement.CreatedAt,
		UpdatedAt:    a.Announcement.UpdatedAt,
	}
}
