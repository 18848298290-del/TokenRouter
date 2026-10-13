package identity

import (
	"context"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Custom attributes store user values as text alongside separate definitions.
// The definition selects requiredness, byte-length checks, numeric bounds,
// pattern checks and allowed selections, while its display fields help account
// forms identify the value being edited. Existing users may have no value for a
// definition marked required.
//
// Value updates validate only the submitted enabled definitions before the
// repository writes them. Multi-select accepts JSON string arrays or
// comma-separated text and retains the supplied representation. Definition
// updates distinguish nil request pointers from supplied zero values on a
// loaded record; persistence receives that edited record rather than a
// per-column patch mask.

// These value categories select attribute editing and validation branches;
// numeric and selection categories have additional checks in
// UserAttributeService.
const (
	AttributeTypeText        UserAttributeType = "text"         // plain text value
	AttributeTypeTextarea    UserAttributeType = "textarea"     // multiline text value
	AttributeTypeNumber      UserAttributeType = "number"       // integer text value
	AttributeTypeEmail       UserAttributeType = "email"        // email value category
	AttributeTypeURL         UserAttributeType = "url"          // URL value category
	AttributeTypeDate        UserAttributeType = "date"         // date value category
	AttributeTypeSelect      UserAttributeType = "select"       // single option value
	AttributeTypeMultiSelect UserAttributeType = "multi_select" // multiple option values
)

// These errors identify missing definitions, duplicate active definition keys
// and unsupported value categories during attribute management and validated
// user updates.
var (
	ErrAttributeDefinitionNotFound = infraerrors.NotFound("ATTRIBUTE_DEFINITION_NOT_FOUND", "attribute definition not found")
	ErrAttributeKeyExists          = infraerrors.Conflict("ATTRIBUTE_KEY_EXISTS", "attribute key already exists")
	ErrInvalidAttributeType        = infraerrors.BadRequest("INVALID_ATTRIBUTE_TYPE", "invalid attribute type")
)

// UserAttributeType identifies how an attribute value is entered and validated.
type UserAttributeType string

// UserAttributeOption pairs an accepted selection value with its displayed
// label.
type UserAttributeOption struct {
	Value string `json:"value"` // accepted selection value text
	Label string `json:"label"` // displayed selection label text
}

// UserAttributeValidation contains optional limits and pattern rules applied to
// submitted text.
type UserAttributeValidation struct {
	// MinLength and MaxLength compare Go string byte counts for the submitted
	// value. Nil disables each bound; empty optional values skip these checks
	// along with the remaining validation rules.
	MinLength *int `json:"min_length,omitempty"` // minimum input byte count
	MaxLength *int `json:"max_length,omitempty"` // maximum input byte count
	// Min and Max apply after integer parsing for number attributes. Nil disables
	// the corresponding bound, and other value categories leave these numeric
	// limits unused while applying their own enabled checks.
	Min *int `json:"min,omitempty"` // minimum parsed integer value
	Max *int `json:"max,omitempty"` // maximum parsed integer value
	// Pattern supplies the raw expression compiled for nonempty value validation.
	// Definition validation checks a trimmed expression, while storage retains the
	// supplied text and value checks compile that retained representation.
	Pattern *string `json:"pattern,omitempty"`
	// Message replaces the default pattern-mismatch text when nonempty. Required,
	// length, numeric, selection and invalid-expression failures use their own
	// messages.
	Message *string `json:"message,omitempty"`
}

// UserAttributeDefinition holds the presentation and validation policy for one
// custom field.
type UserAttributeDefinition struct {
	ID          int64  // stored definition row identifier
	Key         string // unique active definition key
	Name        string // field display and error name
	Description string // field explanatory help text
	// Type selects value validation branches. Email, URL and date categories use
	// the configured common checks; their category alone adds no format-specific
	// test.
	Type UserAttributeType
	// Options supplies accepted literal values for select and multi-select
	// validation, with labels carried separately for the field's displayed
	// choices.
	Options []UserAttributeOption
	// Required rejects empty submitted text. Omitted definitions are not checked,
	// and nonempty JSON text such as [] or null reaches the remaining checks
	// even when multi-select decoding produces no selected values.
	Required     bool
	Validation   UserAttributeValidation // optional submitted value checks
	Placeholder  string                  // form input placeholder text
	DisplayOrder int                     // definition display ordering position
	// Enabled controls whether UpdateUserAttributes can select this definition.
	// Existing saved values remain readable when the definition is disabled.
	Enabled   bool
	CreatedAt time.Time // definition creation timestamp
	UpdatedAt time.Time // definition update timestamp
}

// UserAttributeValue is the stored text belonging to one user and attribute
// definition.
type UserAttributeValue struct {
	ID          int64 // stored value row identifier
	UserID      int64 // owning local account identifier
	AttributeID int64 // owning attribute definition identifier
	// Value retains the submitted text after validation. Multi-select input can be
	// a JSON array string or comma-separated text; saving does not normalize
	// either format.
	Value     string
	CreatedAt time.Time // value creation timestamp
	UpdatedAt time.Time // value update timestamp
}

// CreateAttributeDefinitionInput supplies presentation and value rules for a
// new definition.
type CreateAttributeDefinitionInput struct {
	Key         string            // unique active definition key
	Name        string            // field display and error name
	Description string            // field explanatory help text
	Type        UserAttributeType // selected value category
	// Options is retained by the created domain record without a slice copy.
	// Callers keep its storage stable during creation and manage later mutations
	// themselves.
	Options  []UserAttributeOption
	Required bool // submitted empty value restriction
	// Validation is copied into the new definition as a struct, while its optional
	// pointers remain shared with this input after the construction call.
	Validation  UserAttributeValidation
	Placeholder string // form input placeholder text
	// Enabled is passed to persistence as supplied. Its zero value creates a
	// disabled definition rather than selecting the schema's default enabled
	// value.
	Enabled bool
}

// UpdateAttributeDefinitionInput supplies optional edits applied to a loaded
// definition.
type UpdateAttributeDefinitionInput struct {
	// Name, Description, Type, Options, Required, Validation, Placeholder and
	// Enabled share the patch presence rule: nil retains the loaded field; a
	// supplied pointer selects its value, including empty strings, false and empty
	// lists.
	Name        *string                  // replacement field display name
	Description *string                  // replacement field help text
	Type        *UserAttributeType       // replacement value category
	Options     *[]UserAttributeOption   // replacement selection option list
	Required    *bool                    // replacement empty value restriction
	Validation  *UserAttributeValidation // replacement submitted value checks
	Placeholder *string                  // replacement input placeholder text
	Enabled     *bool                    // replacement definition enabled flag
}

// UpdateUserAttributeInput selects one definition and its submitted replacement
// text value.
type UpdateUserAttributeInput struct {
	AttributeID int64 // owning attribute definition identifier
	// Value retains the submitted text after validation. Multi-select input can be
	// a JSON array string or comma-separated text; saving does not normalize
	// either format.
	Value string // supplied attribute value text
}

// UserAttributeDefinitionRepository stores editable field definitions and their
// display ordering.
type UserAttributeDefinitionRepository interface {
	// Create persists a supplied definition and fills its storage-assigned
	// metadata. The identity service has checked type and pattern input;
	// persistence enforces active-key uniqueness and its field constraints.
	Create(ctx context.Context, def *UserAttributeDefinition) error
	// GetByID reads one currently visible definition by its row identifier. The
	// PostgreSQL adapter reports ErrAttributeDefinitionNotFound for an absent row,
	// with other lookup failures returned for callers to handle.
	GetByID(ctx context.Context, id int64) (*UserAttributeDefinition, error)
	// GetByKey reads one currently visible definition with the exact supplied key.
	// Missing definitions return the adapter's not-found error, allowing profile
	// synchronization to skip an unavailable configured attribute.
	GetByKey(ctx context.Context, key string) (*UserAttributeDefinition, error)
	// Update persists the editable fields of a supplied definition and refreshes
	// UpdatedAt. Callers pass a loaded record after applying optional changes;
	// this write is not a selected-column field mask.
	Update(ctx context.Context, def *UserAttributeDefinition) error
	// Delete removes a definition through repository deletion rules. The
	// PostgreSQL adapter uses the schema's soft-delete handling for ordinary
	// contexts; callers coordinate associated value removal before invoking this
	// operation.
	Delete(ctx context.Context, id int64) error
	// List reads visible definitions in display order, optionally restricted to
	// enabled entries. UpdateUserAttributes requests the enabled subset, while
	// management callers can request every current definition for display and
	// editing.
	List(ctx context.Context, enabledOnly bool) ([]UserAttributeDefinition, error)
	// UpdateDisplayOrders writes the supplied definition-ID to position mapping.
	// The PostgreSQL adapter groups its writes in one transaction and rolls back
	// after a failed row update before returning that failure.
	UpdateDisplayOrders(ctx context.Context, orders map[int64]int) error
	// ExistsByKey reports whether the exact key already names a visible
	// definition. Creation uses this as a preflight read; the persistence
	// uniqueness rule still decides races between concurrent definition creations.
	ExistsByKey(ctx context.Context, key string) (bool, error)
}

// UserAttributeValueRepository stores user text values indexed by their owning
// definition.
type UserAttributeValueRepository interface {
	// GetByUserID reads saved attribute text for one user. Definition enablement
	// is separate from value storage, so existing values remain available even
	// when their definition is currently hidden from editing.
	GetByUserID(ctx context.Context, userID int64) ([]UserAttributeValue, error)
	// GetByUserIDs reads saved values for the requested user identifiers. The
	// PostgreSQL adapter returns an empty list for empty input, and callers group
	// returned records by user and definition IDs.
	GetByUserIDs(ctx context.Context, userIDs []int64) ([]UserAttributeValue, error)
	// UpsertBatch writes supplied text by user and definition without revalidating
	// it. The service validates inputs first; the PostgreSQL adapter uses one
	// transaction to insert or replace all selected values.
	UpsertBatch(ctx context.Context, userID int64, values []UpdateUserAttributeInput) error
	// DeleteByAttributeID removes saved values attached to one definition.
	// Definition deletion calls this first; the PostgreSQL adapter hard-deletes
	// values, while later definition removal retains its separate storage and
	// failure result.
	DeleteByAttributeID(ctx context.Context, attributeID int64) error
	// DeleteByUserID removes every saved attribute value for the selected account.
	// The PostgreSQL adapter hard-deletes those value records; callers decide how
	// this cleanup joins the account's remaining deletion steps.
	DeleteByUserID(ctx context.Context, userID int64) error
}
