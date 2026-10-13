package postgres

import (
	"context"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/userattributedefinition"
	"github.com/TokenFlux/TokenRouter/ent/userattributevalue"
	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// Account attribute definitions persist input kinds, options, and validation
// limits separately from per-account value strings. Ordinary CRUD reads the
// Ent transaction from context when supplied, while display-order and
// batch-value writes open their own transaction to keep their selected
// multirow update together.
//
// Options and validation use JSON-compatible maps in storage. Conversion
// allocates option records and preserves the presence of optional validation
// keys through pointers; string and numeric helper reads tolerate unsupported
// stored types with empty or nil results. Numeric values accept the supported
// integer and floating forms, with Go int conversion applying its usual
// truncation and width. Delete-by-filter operations treat absent rows as
// successful when Ent reports no error.

// userAttributeDefinitionRepository persists administrator input definitions
// and their display order through Ent.
type userAttributeDefinitionRepository struct {
	client *dbent.Client // default attribute persistence client
}

// userAttributeValueRepository persists per-account attribute strings and
// batch value writes through Ent.
type userAttributeValueRepository struct {
	client *dbent.Client // default attribute persistence client
}

// NewUserAttributeDefinitionRepository retains the Ent client used for
// administrator attribute definitions.
func NewUserAttributeDefinitionRepository(client *dbent.Client) identity.UserAttributeDefinitionRepository {
	return &userAttributeDefinitionRepository{client: client}
}

// Create stores the supplied definition and refreshes its ID, display order,
// and row timestamps. It joins a context transaction when available and maps
// recognized unique conflicts to ErrAttributeKeyExists.
func (r *userAttributeDefinitionRepository) Create(ctx context.Context, def *identity.UserAttributeDefinition) error {
	client := clientFromContext(ctx, r.client)

	created, err := client.UserAttributeDefinition.Create().
		SetKey(def.Key).
		SetName(def.Name).
		SetDescription(def.Description).
		SetType(string(def.Type)).
		SetOptions(toEntOptions(def.Options)).
		SetRequired(def.Required).
		SetValidation(toEntValidation(def.Validation)).
		SetPlaceholder(def.Placeholder).
		SetEnabled(def.Enabled).
		Save(ctx)
	if err != nil {
		return translatePersistenceError(err, nil, identity.ErrAttributeKeyExists)
	}

	def.ID = created.ID
	def.DisplayOrder = created.DisplayOrder
	def.CreatedAt = created.CreatedAt
	def.UpdatedAt = created.UpdatedAt
	return nil
}

// GetByID loads one definition using the context transaction client when
// available. Missing records map to ErrAttributeDefinitionNotFound; successful
// conversion allocates option values and optional validation pointers.
func (r *userAttributeDefinitionRepository) GetByID(ctx context.Context, id int64) (*identity.UserAttributeDefinition, error) {
	client := clientFromContext(ctx, r.client)

	e, err := client.UserAttributeDefinition.Query().
		Where(userattributedefinition.IDEQ(id)).
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, identity.ErrAttributeDefinitionNotFound, nil)
	}
	return defEntityToService(e), nil
}

// GetByKey loads the exact supplied definition key using the selected Ent
// client. Missing records map to ErrAttributeDefinitionNotFound, while other
// persistence failures remain errors for the attribute service.
func (r *userAttributeDefinitionRepository) GetByKey(ctx context.Context, key string) (*identity.UserAttributeDefinition, error) {
	client := clientFromContext(ctx, r.client)

	e, err := client.UserAttributeDefinition.Query().
		Where(userattributedefinition.KeyEQ(key)).
		Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, identity.ErrAttributeDefinitionNotFound, nil)
	}
	return defEntityToService(e), nil
}

// Update replaces the supplied definition editable rules and refreshes its
// updated timestamp. The definition key is retained, and missing records or
// recognized conflicts receive the configured attribute errors.
func (r *userAttributeDefinitionRepository) Update(ctx context.Context, def *identity.UserAttributeDefinition) error {
	client := clientFromContext(ctx, r.client)

	updated, err := client.UserAttributeDefinition.UpdateOneID(def.ID).
		SetName(def.Name).
		SetDescription(def.Description).
		SetType(string(def.Type)).
		SetOptions(toEntOptions(def.Options)).
		SetRequired(def.Required).
		SetValidation(toEntValidation(def.Validation)).
		SetPlaceholder(def.Placeholder).
		SetDisplayOrder(def.DisplayOrder).
		SetEnabled(def.Enabled).
		Save(ctx)
	if err != nil {
		return translatePersistenceError(err, identity.ErrAttributeDefinitionNotFound, identity.ErrAttributeKeyExists)
	}

	def.UpdatedAt = updated.UpdatedAt
	return nil
}

// Delete removes definitions matching the supplied ID using the selected Ent
// client. An empty match succeeds when Ent returns no error; recognized lookup
// failures map through the shared persistence translator.
func (r *userAttributeDefinitionRepository) Delete(ctx context.Context, id int64) error {
	client := clientFromContext(ctx, r.client)

	_, err := client.UserAttributeDefinition.Delete().
		Where(userattributedefinition.IDEQ(id)).
		Exec(ctx)
	return translatePersistenceError(err, identity.ErrAttributeDefinitionNotFound, nil)
}

// List returns definitions ordered by display position, optionally filtering
// enabled entries. The selected context client performs the query, and a
// successful empty result returns an allocated domain slice.
func (r *userAttributeDefinitionRepository) List(ctx context.Context, enabledOnly bool) ([]identity.UserAttributeDefinition, error) {
	client := clientFromContext(ctx, r.client)

	q := client.UserAttributeDefinition.Query()
	if enabledOnly {
		q = q.Where(userattributedefinition.EnabledEQ(true))
	}

	entities, err := q.Order(dbent.Asc(userattributedefinition.FieldDisplayOrder)).All(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]identity.UserAttributeDefinition, 0, len(entities))
	for _, e := range entities {
		result = append(result, *defEntityToService(e))
	}
	return result, nil
}

// UpdateDisplayOrders saves the supplied definition positions inside a newly
// owned Ent transaction. The caller supplies the selected IDs and order
// values, and map iteration does not establish update order.
//
// Each saved row participates in that transaction; a missing row or later SQL
// failure returns before commit and triggers rollback of earlier positions. A
// transaction is opened even for an empty map. Successful completion commits
// all selected writes, with deferred rollback cleanup errors discarded after
// the returned operation result.
func (r *userAttributeDefinitionRepository) UpdateDisplayOrders(ctx context.Context, orders map[int64]int) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	// An owned transaction groups the selected row writes. A later failure rolls
	// back earlier values or positions, while deferred cleanup errors do not
	// replace the operation result.
	defer func() { _ = tx.Rollback() }()

	for id, order := range orders {
		if _, err := tx.UserAttributeDefinition.UpdateOneID(id).
			SetDisplayOrder(order).
			Save(ctx); err != nil {
			return translatePersistenceError(err, identity.ErrAttributeDefinitionNotFound, nil)
		}
	}

	// Commit every requested definition position after all row updates succeed,
	// keeping a later missing-row failure from leaving a partially reordered
	// stored definition set.
	return tx.Commit()
}

// ExistsByKey reports exact definition-key presence through the selected Ent
// client.
func (r *userAttributeDefinitionRepository) ExistsByKey(ctx context.Context, key string) (bool, error) {
	client := clientFromContext(ctx, r.client)
	return client.UserAttributeDefinition.Query().
		Where(userattributedefinition.KeyEQ(key)).
		Exist(ctx)
}

// NewUserAttributeValueRepository retains the Ent client used for per-account
// attribute strings.
func NewUserAttributeValueRepository(client *dbent.Client) identity.UserAttributeValueRepository {
	return &userAttributeValueRepository{client: client}
}

// GetByUserID returns stored attribute strings for the supplied account using
// the selected Ent client. The result preserves row values and timestamps,
// with an allocated empty slice when no rows match.
func (r *userAttributeValueRepository) GetByUserID(ctx context.Context, userID int64) ([]identity.UserAttributeValue, error) {
	client := clientFromContext(ctx, r.client)

	entities, err := client.UserAttributeValue.Query().
		Where(userattributevalue.UserIDEQ(userID)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]identity.UserAttributeValue, 0, len(entities))
	for _, e := range entities {
		result = append(result, identity.UserAttributeValue{
			ID:          e.ID,
			UserID:      e.UserID,
			AttributeID: e.AttributeID,
			Value:       e.Value,
			CreatedAt:   e.CreatedAt,
			UpdatedAt:   e.UpdatedAt,
		})
	}
	return result, nil
}

// GetByUserIDs returns stored attribute strings for the supplied account
// selection. Empty input returns an allocated empty slice before querying; the
// Ent client is selected from context when available.
func (r *userAttributeValueRepository) GetByUserIDs(ctx context.Context, userIDs []int64) ([]identity.UserAttributeValue, error) {
	if len(userIDs) == 0 {
		return []identity.UserAttributeValue{}, nil
	}

	client := clientFromContext(ctx, r.client)

	entities, err := client.UserAttributeValue.Query().
		Where(userattributevalue.UserIDIn(userIDs...)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]identity.UserAttributeValue, 0, len(entities))
	for _, e := range entities {
		result = append(result, identity.UserAttributeValue{
			ID:          e.ID,
			UserID:      e.UserID,
			AttributeID: e.AttributeID,
			Value:       e.Value,
			CreatedAt:   e.CreatedAt,
			UpdatedAt:   e.UpdatedAt,
		})
	}
	return result, nil
}

// UpsertBatch saves supplied definition strings for one account in a newly
// owned transaction. Empty input succeeds before transaction creation, and the
// account/definition pair selects conflict replacement.
//
// Entries run in input order, so a repeated pair later in the batch replaces
// its preceding value. Each upsert updates the saved timestamp with its value.
// Any failure returns before commit and deferred rollback covers earlier
// entries; success commits the complete batch. An already-bound configured
// client must still support the new-transaction call used here.
func (r *userAttributeValueRepository) UpsertBatch(ctx context.Context, userID int64, inputs []identity.UpdateUserAttributeInput) error {
	if len(inputs) == 0 {
		return nil
	}

	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	// An owned transaction groups the selected row writes. A later failure rolls
	// back earlier values or positions, while deferred cleanup errors do not
	// replace the operation result.
	defer func() { _ = tx.Rollback() }()

	for _, input := range inputs {
		err := tx.UserAttributeValue.Create().
			SetUserID(userID).
			SetAttributeID(input.AttributeID).
			SetValue(input.Value).
			OnConflictColumns(userattributevalue.FieldUserID, userattributevalue.FieldAttributeID).
			UpdateValue().
			UpdateUpdatedAt().
			Exec(ctx)
		if err != nil {
			return err
		}
	}

	// Commit account attribute value and timestamp replacements after every
	// supplied entry succeeds, so a later failing pair does not leave earlier
	// entries from this batch saved.
	return tx.Commit()
}

// DeleteByAttributeID removes value rows for the supplied definition using the
// selected Ent client. Missing value rows succeed when Ent reports no error;
// other persistence failures pass to the caller.
func (r *userAttributeValueRepository) DeleteByAttributeID(ctx context.Context, attributeID int64) error {
	client := clientFromContext(ctx, r.client)

	_, err := client.UserAttributeValue.Delete().
		Where(userattributevalue.AttributeIDEQ(attributeID)).
		Exec(ctx)
	return err
}

// DeleteByUserID removes value rows for the supplied account using the
// selected Ent client. Missing value rows succeed when Ent reports no error;
// other persistence failures pass to the caller.
func (r *userAttributeValueRepository) DeleteByUserID(ctx context.Context, userID int64) error {
	client := clientFromContext(ctx, r.client)

	_, err := client.UserAttributeValue.Delete().
		Where(userattributevalue.UserIDEQ(userID)).
		Exec(ctx)
	return err
}

// defEntityToService converts a stored definition and returns nil for nil
// input. Options receive new records, and supported present validation fields
// receive domain pointers; other scalar fields and timestamps copy directly.
func defEntityToService(e *dbent.UserAttributeDefinition) *identity.UserAttributeDefinition {
	if e == nil {
		return nil
	}
	return &identity.UserAttributeDefinition{
		ID:           e.ID,
		Key:          e.Key,
		Name:         e.Name,
		Description:  e.Description,
		Type:         identity.UserAttributeType(e.Type),
		Options:      toServiceOptions(e.Options),
		Required:     e.Required,
		Validation:   toServiceValidation(e.Validation),
		Placeholder:  e.Placeholder,
		DisplayOrder: e.DisplayOrder,
		Enabled:      e.Enabled,
		CreatedAt:    e.CreatedAt,
		UpdatedAt:    e.UpdatedAt,
	}
}

// toEntOptions allocates storage maps for selectable value and label strings.
// Nil input becomes an allocated empty list, so storage receives the same
// collection shape as an explicitly empty option list.
func toEntOptions(opts []identity.UserAttributeOption) []map[string]any {
	if opts == nil {
		return []map[string]any{}
	}
	result := make([]map[string]any, len(opts))
	for i, o := range opts {
		result[i] = map[string]any{"value": o.Value, "label": o.Label}
	}
	return result
}

// toServiceOptions allocates domain option values from the supported stored
// string fields. Nil input becomes an allocated empty list, while missing or
// nonstring labels and values read as empty strings.
func toServiceOptions(opts []map[string]any) []identity.UserAttributeOption {
	if opts == nil {
		return []identity.UserAttributeOption{}
	}
	result := make([]identity.UserAttributeOption, len(opts))
	for i, o := range opts {
		result[i] = identity.UserAttributeOption{
			Value: getString(o, "value"),
			Label: getString(o, "label"),
		}
	}
	return result
}

// toEntValidation allocates a storage map containing each present validation
// pointer value. Nil pointers omit their keys, while pointed zero or empty
// values remain present for later definition reads.
func toEntValidation(v identity.UserAttributeValidation) map[string]any {
	result := map[string]any{}
	if v.MinLength != nil {
		result["min_length"] = *v.MinLength
	}
	if v.MaxLength != nil {
		result["max_length"] = *v.MaxLength
	}
	if v.Min != nil {
		result["min"] = *v.Min
	}
	if v.Max != nil {
		result["max"] = *v.Max
	}
	if v.Pattern != nil {
		result["pattern"] = *v.Pattern
	}
	if v.Message != nil {
		result["message"] = *v.Message
	}
	return result
}

// toServiceValidation creates pointers for supported present validation map
// values. Unsupported or absent numeric and string values remain nil, and
// integer conversion determines the saved domain bounds returned to attribute
// validation.
func toServiceValidation(v map[string]any) identity.UserAttributeValidation {
	result := identity.UserAttributeValidation{}
	if val := getInt(v, "min_length"); val != nil {
		result.MinLength = val
	}
	if val := getInt(v, "max_length"); val != nil {
		result.MaxLength = val
	}
	if val := getInt(v, "min"); val != nil {
		result.Min = val
	}
	if val := getInt(v, "max"); val != nil {
		result.Max = val
	}
	if val := getStringPtr(v, "pattern"); val != nil {
		result.Pattern = val
	}
	if val := getStringPtr(v, "message"); val != nil {
		result.Message = val
	}
	return result
}

// getString returns a stored string or empty text for an absent or differently
// typed value. Option conversion uses this tolerant result for label and value
// fields before domain consumers read them.
func getString(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	// Unsupported stored option text contributes an empty label or value to the
	// converted definition, allowing its remaining fields to be decoded from the
	// same stored option record.
	return ""
}

// getStringPtr returns a pointer to present string text, including empty text.
// Missing or differently typed entries return nil, preserving the
// optional-field distinction used by definition validation conversion.
func getStringPtr(m map[string]any, key string) *string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return &s
		}
	}
	// An absent or differently typed optional rule leaves its pointer unset.
	// Definition conversion therefore retains the usable pattern/message fields
	// without interpreting another stored JSON type as text.
	return nil
}

// getInt returns an int pointer for supported int, int64, or float64 map
// values. Missing or unsupported values return nil; conversion uses Go int
// width and truncates representable fractional floats without separate range
// validation.
func getInt(m map[string]any, key string) *int {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case int:
			return &n
		// Stored integer limits narrow to the platform int width.
		case int64:
			i := int(n)
			return &i
		// Stored JSON numeric limits can arrive as float64. Representable values
		// lose their fractional part when converted; range validation is absent, so
		// out-of-range inputs keep Go's conversion behavior.
		case float64:
			i := int(n)
			return &i
		}
	}
	// Unsupported numeric storage types supply no optional bound. The converted
	// definition leaves that limit unset for value validation rather than parsing
	// another representation into an integer here.
	return nil
}
