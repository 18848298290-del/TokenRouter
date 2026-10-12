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

// UserAttributeService handles attribute management.
type UserAttributeService struct {
	defRepo   UserAttributeDefinitionRepository
	valueRepo UserAttributeValueRepository
}

// NewUserAttributeService binds definition and value storage for attribute management.
func NewUserAttributeService(
	defRepo UserAttributeDefinitionRepository,
	valueRepo UserAttributeValueRepository,
) *UserAttributeService {
	return &UserAttributeService{
		defRepo:   defRepo,
		valueRepo: valueRepo,
	}
}

// CreateDefinition creates a new attribute definition.
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

// GetDefinitionByKey retrieves a definition by its unique key.
func (s *UserAttributeService) GetDefinitionByKey(ctx context.Context, key string) (*UserAttributeDefinition, error) {
	return s.defRepo.GetByKey(ctx, key)
}

// ListDefinitions lists definitions, optionally restricted to enabled ones.
func (s *UserAttributeService) ListDefinitions(ctx context.Context, enabledOnly bool) ([]UserAttributeDefinition, error) {
	return s.defRepo.List(ctx, enabledOnly)
}

// UpdateDefinition updates an existing definition.
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

// DeleteDefinition soft-deletes a definition and hard-deletes associated values.
func (s *UserAttributeService) DeleteDefinition(ctx context.Context, id int64) error {
	_, err := s.defRepo.GetByID(ctx, id)
	if err != nil {
		return err
	}

	if err := s.valueRepo.DeleteByAttributeID(ctx, id); err != nil {
		return fmt.Errorf("delete values: %w", err)
	}

	if err := s.defRepo.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete definition: %w", err)
	}

	return nil
}

// ReorderDefinitions updates display order for multiple definitions.
func (s *UserAttributeService) ReorderDefinitions(ctx context.Context, orders map[int64]int) error {
	return s.defRepo.UpdateDisplayOrders(ctx, orders)
}

// GetUserAttributes retrieves all attribute values for a user.
func (s *UserAttributeService) GetUserAttributes(ctx context.Context, userID int64) ([]UserAttributeValue, error) {
	return s.valueRepo.GetByUserID(ctx, userID)
}

// GetBatchUserAttributes returns values indexed by user ID, then attribute ID.
// Users with no stored values are absent from the result.
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

// UpdateUserAttributes batch updates attribute values for a user.
func (s *UserAttributeService) UpdateUserAttributes(ctx context.Context, userID int64, inputs []UpdateUserAttributeInput) error {
	// Validate all values before updating
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

// validateValue validates a value against its definition.
func (s *UserAttributeService) validateValue(def *UserAttributeDefinition, value string) error {
	// Skip validation for empty non-required fields
	if value == "" && !def.Required {
		return nil
	}

	if def.Required && value == "" {
		return ValidationError(fmt.Sprintf("%s is required", def.Name))
	}

	v := def.Validation

	// Length limits count bytes, matching Go string length.
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

	// Accept a JSON string array or comma-separated values for multi-select attributes.
	if def.Type == AttributeTypeMultiSelect && value != "" {
		var values []string
		if err := json.Unmarshal([]byte(value), &values); err != nil {
			// Non-JSON input is interpreted as comma-separated values.
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

// ValidationError creates a validation error with a custom message.
func ValidationError(msg string) error {
	return infraerrors.BadRequest("ATTRIBUTE_VALIDATION_FAILED", msg)
}

func IsValidAttributeType(t UserAttributeType) bool {
	switch t {
	case AttributeTypeText, AttributeTypeTextarea, AttributeTypeNumber,
		AttributeTypeEmail, AttributeTypeURL, AttributeTypeDate,
		AttributeTypeSelect, AttributeTypeMultiSelect:
		return true
	}
	return false
}

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
