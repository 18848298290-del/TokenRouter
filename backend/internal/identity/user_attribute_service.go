package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// user_attribute_service.go validates attribute definitions and submitted
// account values before requesting persistence.
// Attribute edits use a definition's current rules to validate the submitted
// text before requesting a value write. Requiredness applies to supplied
// inputs, byte limits use Go string length, numeric values use integer parsing,
// and selections compare against configured literal options. Other categories
// use the common checks and any configured pattern; their names add no implicit
// email, URL or date parser.
//
// Definition changes apply optional pointers to a loaded record, then persist
// that editable view. Value batches validate their complete submitted set
// before delegating atomic writes to the repository. Definition removal instead
// calls value deletion before definition deletion, leaving their transaction
// grouping with the caller's context and repository behavior. Regex validation
// of definitions trims input for checking but retains the supplied pattern
// text.

// UserAttributeService manages definitions and validates user text before
// repository writes.
type UserAttributeService struct {
	defRepo   UserAttributeDefinitionRepository // current field definition repository
	valueRepo UserAttributeValueRepository      // user attribute value repository
}

// NewUserAttributeService retains the repositories for definition management
// and user values.
func NewUserAttributeService(
	defRepo UserAttributeDefinitionRepository,
	valueRepo UserAttributeValueRepository,
) *UserAttributeService {
	return &UserAttributeService{
		defRepo:   defRepo,
		valueRepo: valueRepo,
	}
}

// CreateDefinition validates and persists a new custom attribute definition. It
// requires a supported type, an unused active key and a compilable configured
// pattern; the repository fills the returned record's storage metadata. Options
// and validation pointers remain shared with the supplied input representation.
func (s *UserAttributeService) CreateDefinition(ctx context.Context, input CreateAttributeDefinitionInput) (*UserAttributeDefinition, error) {
	if !IsValidAttributeType(input.Type) {
		return nil, ErrInvalidAttributeType
	}

	exists, err := s.defRepo.ExistsByKey(ctx, input.Key)
	if err != nil {
		return nil, fmt.Errorf("check key exists: %w", err)
	}
	if exists {
		return nil, ErrAttributeKeyExists
	}

	def := &UserAttributeDefinition{
		Key:         input.Key,
		Name:        input.Name,
		Description: input.Description,
		Type:        input.Type,
		Options:     input.Options,
		Required:    input.Required,
		Validation:  input.Validation,
		Placeholder: input.Placeholder,
		Enabled:     input.Enabled,
	}

	if err := ValidateDefinitionPattern(def); err != nil {
		return nil, err
	}

	if err := s.defRepo.Create(ctx, def); err != nil {
		return nil, fmt.Errorf("create definition: %w", err)
	}

	return def, nil
}

// GetDefinitionByKey reads the current definition with the supplied exact key.
// The PostgreSQL adapter reports ErrAttributeDefinitionNotFound for a missing
// definition, which profile synchronization uses to skip an unavailable
// configured field.
func (s *UserAttributeService) GetDefinitionByKey(ctx context.Context, key string) (*UserAttributeDefinition, error) {
	return s.defRepo.GetByKey(ctx, key)
}

// ListDefinitions returns current definitions from the repository's display
// order. enabledOnly selects the editable subset when true; false includes
// disabled definitions so management can display and change their current
// configuration.
func (s *UserAttributeService) ListDefinitions(ctx context.Context, enabledOnly bool) ([]UserAttributeDefinition, error) {
	return s.defRepo.List(ctx, enabledOnly)
}

// UpdateDefinition applies supplied optional changes to a loaded definition and
// persists that edited record. Nil pointers retain values from this read, while
// non-nil pointers can set empty strings, false or empty option lists.
//
// A changed type is checked against supported categories, and the resulting
// pattern is checked before persistence. Options and validation pointers are
// retained from input. The repository writes the editable view, so concurrent
// updates can replace values from an earlier read rather than merge each
// omitted field against its latest database value. An all-nil input still
// delegates persistence of the loaded record rather than return as a no-op.
func (s *UserAttributeService) UpdateDefinition(ctx context.Context, id int64, input UpdateAttributeDefinitionInput) (*UserAttributeDefinition, error) {
	def, err := s.defRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if input.Name != nil {
		def.Name = *input.Name
	}
	if input.Description != nil {
		def.Description = *input.Description
	}
	if input.Type != nil {
		if !IsValidAttributeType(*input.Type) {
			return nil, ErrInvalidAttributeType
		}
		def.Type = *input.Type
	}
	if input.Options != nil {
		def.Options = *input.Options
	}
	if input.Required != nil {
		def.Required = *input.Required
	}
	if input.Validation != nil {
		def.Validation = *input.Validation
	}
	if input.Placeholder != nil {
		def.Placeholder = *input.Placeholder
	}
	if input.Enabled != nil {
		def.Enabled = *input.Enabled
	}

	if err := ValidateDefinitionPattern(def); err != nil {
		return nil, err
	}

	if err := s.defRepo.Update(ctx, def); err != nil {
		return nil, fmt.Errorf("update definition: %w", err)
	}

	return def, nil
}

// DeleteDefinition removes associated user values before deleting the
// definition. It first confirms a visible definition exists, then returns
// immediately on a value-deletion failure before requesting the definition's
// soft deletion.
//
// The two mutations are separate repository calls using the supplied context.
// This service leaves transaction grouping with its caller and adapters; when
// the calls run independently, a later definition failure can follow values
// already removed. Callers requiring one rollback scope must supply that scope
// across the participating repository operations.
func (s *UserAttributeService) DeleteDefinition(ctx context.Context, id int64) error {
	_, err := s.defRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}

	// Remove values before the definition so its successful removal does not leave
	// these associated records behind; a failure here stops the later delete.
	if err := s.valueRepo.DeleteByAttributeID(ctx, id); err != nil {
		return fmt.Errorf("delete values: %w", err)
	}

	if err := s.defRepo.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete definition: %w", err)
	}

	return nil
}

// ReorderDefinitions delegates the supplied definition-ID to display-position
// writes to storage.
func (s *UserAttributeService) ReorderDefinitions(ctx context.Context, orders map[int64]int) error {
	return s.defRepo.UpdateDisplayOrders(ctx, orders)
}

// GetUserAttributes reads the stored custom text values belonging to one user.
func (s *UserAttributeService) GetUserAttributes(ctx context.Context, userID int64) ([]UserAttributeValue, error) {
	return s.valueRepo.GetByUserID(ctx, userID)
}

// GetBatchUserAttributes groups saved text by user ID and then definition ID.
// Users with no returned records have no map entry, while repeated records for
// one pair replace its earlier text in the returned repository order.
func (s *UserAttributeService) GetBatchUserAttributes(ctx context.Context, userIDs []int64) (map[int64]map[int64]string, error) {
	values, err := s.valueRepo.GetByUserIDs(ctx, userIDs)
	if err != nil {
		return nil, err
	}

	result := make(map[int64]map[int64]string)
	for _, v := range values {
		if result[v.UserID] == nil {
			result[v.UserID] = make(map[int64]string)
		}
		result[v.UserID][v.AttributeID] = v.Value
	}

	return result, nil
}

// UpdateUserAttributes validates the submitted enabled attributes before
// writing their values as one repository batch. Every supplied definition ID
// must belong to the enabled list, and each submitted text value must pass that
// definition's applicable required, limit, pattern and selection checks.
//
// Only supplied attributes are checked, so omitted required definitions are not
// filled or rejected by this partial update. Empty input still reads
// definitions before reaching the repository's empty-batch handling.
// Persistence receives the submitted representations unchanged, and the
// repository owns its atomic write and rollback rules after validation
// succeeds.
func (s *UserAttributeService) UpdateUserAttributes(ctx context.Context, userID int64, inputs []UpdateUserAttributeInput) error {
	// Load one definition list for validating this submitted batch, then finish
	// every supplied value check before a repository write can persist any of
	// them.
	defs, err := s.defRepo.List(ctx, true)
	if err != nil {
		return fmt.Errorf("list definitions: %w", err)
	}

	defMap := make(map[int64]*UserAttributeDefinition, len(defs))
	for i := range defs {
		defMap[defs[i].ID] = &defs[i]
	}

	for _, input := range inputs {
		def, ok := defMap[input.AttributeID]
		if !ok {
			return ErrAttributeDefinitionNotFound
		}

		if err := s.validateValue(def, input.Value); err != nil {
			return err
		}
	}

	return s.valueRepo.UpsertBatch(ctx, userID, inputs)
}

// validateValue checks submitted text under one attribute definition's rules.
// Empty optional values succeed before all other checks; required empty text
// fails. Length limits count bytes, number values require integer parsing, and
// a configured expression is compiled as stored for nonempty text.
//
// Select accepts one literal option value. Multi-select first tries a JSON
// string array, then splits undecodable input on commas and trims each selected
// value before membership checks. JSON null decodes to an empty list and
// therefore has no option checks; requiredness still sees its nonempty original
// text. These checks leave the supplied representation unchanged for storage,
// and categories such as email or date add no format-specific parser.
func (s *UserAttributeService) validateValue(def *UserAttributeDefinition, value string) error {
	// Optional empty text represents a cleared value, so skip the remaining
	// limits, pattern and option checks that apply when this field supplies
	// content.
	if value == "" && !def.Required {
		return nil
	}

	if def.Required && value == "" {
		return ValidationError(fmt.Sprintf("%s is required", def.Name))
	}

	v := def.Validation

	// Limits use stored string bytes, so multibyte text can reach a bound with
	// fewer displayed characters than the integer named in the validation error.
	if v.MinLength != nil && len(value) < *v.MinLength {
		return ValidationError(fmt.Sprintf("%s must be at least %d characters", def.Name, *v.MinLength))
	}
	if v.MaxLength != nil && len(value) > *v.MaxLength {
		return ValidationError(fmt.Sprintf("%s must be at most %d characters", def.Name, *v.MaxLength))
	}

	if def.Type == AttributeTypeNumber && value != "" {
		num, err := strconv.Atoi(value)
		if err != nil {
			return ValidationError(fmt.Sprintf("%s must be a number", def.Name))
		}
		if v.Min != nil && num < *v.Min {
			return ValidationError(fmt.Sprintf("%s must be at least %d", def.Name, *v.Min))
		}
		if v.Max != nil && num > *v.Max {
			return ValidationError(fmt.Sprintf("%s must be at most %d", def.Name, *v.Max))
		}
	}

	if v.Pattern != nil && *v.Pattern != "" && value != "" {
		re, err := regexp.Compile(*v.Pattern)
		if err != nil {
			return ValidationError(def.Name + " has an invalid pattern")
		}
		if !re.MatchString(value) {
			msg := def.Name + " format is invalid"
			if v.Message != nil && *v.Message != "" {
				msg = *v.Message
			}
			return ValidationError(msg)
		}
	}

	if def.Type == AttributeTypeSelect && value != "" {
		found := false
		for _, opt := range def.Options {
			if opt.Value == value {
				found = true
				break
			}
		}
		if !found {
			return ValidationError(fmt.Sprintf("%s: invalid option", def.Name))
		}
	}

	// Multi-select supports the stored JSON-array representation and the
	// comma-separated input representation; validation checks members while
	// persistence keeps the caller's text format unchanged after those members are
	// accepted.
	if def.Type == AttributeTypeMultiSelect && value != "" {
		var values []string
		if err := json.Unmarshal([]byte(value), &values); err != nil {
			// When JSON string-array decoding fails, split the supplied text on commas
			// so clients using that accepted representation still reach option checks.
			values = strings.Split(value, ",")
		}
		for _, val := range values {
			val = strings.TrimSpace(val)
			found := false
			for _, opt := range def.Options {
				if opt.Value == val {
					found = true
					break
				}
			}
			if !found {
				return ValidationError(fmt.Sprintf("%s: invalid option %s", def.Name, val))
			}
		}
	}

	return nil
}

// ValidationError returns an attribute-value bad-request error with the
// supplied message.
func ValidationError(msg string) error {
	return infraerrors.BadRequest("ATTRIBUTE_VALIDATION_FAILED", msg)
}

// IsValidAttributeType reports whether the category is supported by attribute
// definitions.
func IsValidAttributeType(t UserAttributeType) bool {
	switch t {
	case AttributeTypeText, AttributeTypeTextarea, AttributeTypeNumber,
		AttributeTypeEmail, AttributeTypeURL, AttributeTypeDate,
		AttributeTypeSelect, AttributeTypeMultiSelect:
		return true
	}
	return false
}

// ValidateDefinitionPattern checks a configured expression before a definition
// write. Nil definitions, nil patterns and blank trimmed text succeed; nonblank
// text must compile after trimming. The supplied definition and stored pattern
// text are left unchanged by this preliminary check.
func ValidateDefinitionPattern(def *UserAttributeDefinition) error {
	if def == nil {
		return nil
	}
	if def.Validation.Pattern == nil {
		return nil
	}
	pattern := strings.TrimSpace(*def.Validation.Pattern)
	if pattern == "" {
		return nil
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return infraerrors.BadRequest("INVALID_ATTRIBUTE_PATTERN", fmt.Sprintf("invalid pattern for %s: %v", def.Name, err))
	}
	return nil
}
