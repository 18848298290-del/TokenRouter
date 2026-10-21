// userAttributes.types.ts defines custom profile fields, validation settings, and stored
// user values. Field definitions select an input format and optional choices, while
// values remain serialized strings indexed by the attribute identifier when submitted or
// displayed for an account.

// UserAttributeType selects the input and validation format for a custom user attribute.
export type UserAttributeType =
  'text' | 'textarea' | 'number' | 'email' | 'url' | 'date' | 'select' | 'multi_select'

// UserAttributeOption supplies a stored choice value and its displayed attribute label.
export interface UserAttributeOption {
  value: string // Stored choice value.
  label: string // Human-readable display label.
  [key: string]: unknown // Additional indexed record values.
}

// UserAttributeValidation configures numeric, textual, and pattern constraints for an
// attribute value.
export interface UserAttributeValidation {
  min_length?: number // Minimum text length.
  max_length?: number // Maximum text length.
  min?: number // Minimum permitted value.
  max?: number // Maximum permitted value.
  pattern?: string // Validation regular expression.
  message?: string // Validation failure message.
}

// UserAttributeDefinition describes a custom profile field and its form presentation
// settings.
export interface UserAttributeDefinition {
  id: number // Record numeric identifier.
  key: string // Stable attribute identifier.
  name: string // Record display name.
  description: string // Human-readable record description.
  type: UserAttributeType // Attribute input format.
  options: UserAttributeOption[] // Selectable attribute choices.
  required: boolean // Mandatory value flag.
  validation: UserAttributeValidation // Attribute validation rules.
  placeholder: string // Empty input prompt.
  display_order: number // Attribute display position.
  enabled: boolean // Feature activation switch.
  created_at: string // Record creation timestamp.
  updated_at: string // Record update timestamp.
}

// UserAttributeValue stores one account's serialized value for a custom profile
// attribute.
export interface UserAttributeValue {
  id: number // Record numeric identifier.
  user_id: number // Owning account identifier.
  attribute_id: number // Custom attribute identifier.
  value: string // Serialized attribute value.
  created_at: string // Record creation timestamp.
  updated_at: string // Record update timestamp.
}

// CreateUserAttributeRequest supplies a custom profile field and its initial validation
// settings.
export interface CreateUserAttributeRequest {
  key: string // Stable attribute identifier.
  name: string // Record display name.
  description?: string // Human-readable record description.
  type: UserAttributeType // Attribute input format.
  options?: UserAttributeOption[] // Selectable attribute choices.
  required?: boolean // Mandatory value flag.
  validation?: UserAttributeValidation // Attribute validation rules.
  placeholder?: string // Empty input prompt.
  display_order?: number // Attribute display position.
  enabled?: boolean // Feature activation switch.
}

// UpdateUserAttributeRequest supplies optional changes to a custom field and its
// validation.
export interface UpdateUserAttributeRequest {
  key?: string // Stable attribute identifier.
  name?: string // Record display name.
  description?: string // Human-readable record description.
  type?: UserAttributeType // Attribute input format.
  options?: UserAttributeOption[] // Selectable attribute choices.
  required?: boolean // Mandatory value flag.
  validation?: UserAttributeValidation // Attribute validation rules.
  placeholder?: string // Empty input prompt.
  display_order?: number // Attribute display position.
  enabled?: boolean // Feature activation switch.
}

// UserAttributeValuesMap indexes serialized custom profile values by their numeric
// attribute identifiers.
export interface UserAttributeValuesMap {
  [attributeId: number]: string // Values by attribute identifier.
}
