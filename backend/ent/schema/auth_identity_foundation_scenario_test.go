package schema

import (
	"testing"

	"entgo.io/ent"
	"entgo.io/ent/entc/load"
	"entgo.io/ent/schema/field"
	"github.com/stretchr/testify/require"
)

// Login identity schemas describe external subjects, temporary completion state,
// and session-scoped profile choices. The foundation scenario loads Ent metadata
// for those entities and checks field and index declarations, then invokes the
// user signup-source validator directly.

// TestAuthIdentityFoundationSchemas loads identity, channel, pending, adoption,
// and user schema declarations. It checks required field names and selected
// unique indexes, then requires the email signup default and seven accepted
// source values while rejecting twitter.
func TestAuthIdentityFoundationSchemas(t *testing.T) {
	spec, err := (&load.Config{Path: "."}).Load()
	require.NoError(t, err)

	schemas := map[string]*load.Schema{}
	for _, schema := range spec.Schemas {
		schemas[schema.Name] = schema
	}

	authIdentity := requireSchema(t, schemas, "AuthIdentity")
	requireSchemaFields(t, authIdentity,
		"user_id",
		"provider_type",
		"provider_key",
		"provider_subject",
		"verified_at",
		"issuer",
		"metadata",
	)
	requireHasUniqueIndex(t, authIdentity, "provider_type", "provider_key", "provider_subject")

	authIdentityChannel := requireSchema(t, schemas, "AuthIdentityChannel")
	requireSchemaFields(t, authIdentityChannel,
		"identity_id",
		"provider_type",
		"provider_key",
		"channel",
		"channel_app_id",
		"channel_subject",
		"metadata",
	)
	requireHasUniqueIndex(t, authIdentityChannel, "provider_type", "provider_key", "channel", "channel_app_id", "channel_subject")

	pendingAuthSession := requireSchema(t, schemas, "PendingAuthSession")
	requireSchemaFields(t, pendingAuthSession,
		"intent",
		"provider_type",
		"provider_key",
		"provider_subject",
		"target_user_id",
		"redirect_to",
		"resolved_email",
		"registration_password_hash",
		"upstream_identity_claims",
		"local_flow_state",
		"browser_session_key",
		"completion_code_hash",
		"completion_code_expires_at",
		"email_verified_at",
		"password_verified_at",
		"totp_verified_at",
		"expires_at",
		"consumed_at",
	)

	adoptionDecision := requireSchema(t, schemas, "IdentityAdoptionDecision")
	requireSchemaFields(t, adoptionDecision,
		"pending_auth_session_id",
		"identity_id",
		"adopt_display_name",
		"adopt_avatar",
		"decided_at",
	)
	requireHasUniqueIndex(t, adoptionDecision, "pending_auth_session_id")

	userSchema := requireSchema(t, schemas, "User")
	requireSchemaFields(t, userSchema, "signup_source", "last_login_at", "last_active_at")
	signupSource := requireSchemaField(t, userSchema, "signup_source")
	require.Equal(t, field.TypeString, signupSource.Info.Type)
	require.True(t, signupSource.Default)
	require.Equal(t, "email", signupSource.DefaultValue)
	require.Equal(t, 1, signupSource.Validators)

	validator := requireStringFieldValidator(t, User{}.Fields(), "signup_source")
	for _, value := range []string{"email", "linuxdo", "wechat", "oidc", "github", "google", "dingtalk"} {
		require.NoError(t, validator(value))
	}
	require.Error(t, validator("twitter"))
}

// requireSchema returns the loaded schema entry with the requested entity name.
// A missing map entry stops the calling test through require.True. The
// returned pointer is the entry already held in the loaded schema map.
func requireSchema(t *testing.T, schemas map[string]*load.Schema, name string) *load.Schema {
	t.Helper()

	schema, ok := schemas[name]
	require.True(t, ok, "schema %s should exist", name)
	return schema
}

// requireSchemaFields requires the loaded entity to contain every requested field.
// The comparison uses field names and permits additional declarations in the
// schema. Missing requested names stop the calling test.
func requireSchemaFields(t *testing.T, schema *load.Schema, names ...string) {
	t.Helper()

	fields := map[string]struct{}{}
	for _, field := range schema.Fields {
		fields[field.Name] = struct{}{}
	}

	for _, name := range names {
		_, ok := fields[name]
		require.True(t, ok, "schema %s should include field %s", schema.Name, name)
	}
}

// requireSchemaField returns the loaded field descriptor with the requested name.
// It returns the first matching declaration from schema.Fields. A missing
// field stops the calling test through require.Failf.
func requireSchemaField(t *testing.T, schema *load.Schema, name string) *load.Field {
	t.Helper()

	for _, schemaField := range schema.Fields {
		if schemaField.Name == name {
			return schemaField
		}
	}

	require.Failf(t, "missing schema field", "schema %s should include field %s", schema.Name, name)
	return nil
}

// requireStringFieldValidator returns the first validator on the named Ent field.
// The field must expose at least one validator of func(string) error at that
// position; missing fields, validators, or incompatible types stop the test.
func requireStringFieldValidator(t *testing.T, fields []ent.Field, name string) func(string) error {
	t.Helper()

	for _, entField := range fields {
		descriptor := entField.Descriptor()
		if descriptor.Name != name {
			continue
		}
		require.NotEmpty(t, descriptor.Validators, "field %s should include a validator", name)
		validator, ok := descriptor.Validators[0].(func(string) error)
		require.True(t, ok, "field %s validator should be func(string) error", name)
		return validator
	}

	require.Failf(t, "missing field validator", "schema should include field %s", name)
	return nil
}

// requireHasUniqueIndex requires a unique index with the supplied field sequence.
// Both field count and order must match the loaded index declaration; other
// indexes are skipped, and no matching declaration stops the calling test.
func requireHasUniqueIndex(t *testing.T, schema *load.Schema, fields ...string) {
	t.Helper()

	for _, index := range schema.Indexes {
		if !index.Unique {
			continue
		}
		if len(index.Fields) != len(fields) {
			continue
		}
		match := true
		for i := range fields {
			if index.Fields[i] != fields[i] {
				match = false
				break
			}
		}
		if match {
			return
		}
	}

	require.Failf(t, "missing unique index", "schema %s should include unique index on %v", schema.Name, fields)
}
