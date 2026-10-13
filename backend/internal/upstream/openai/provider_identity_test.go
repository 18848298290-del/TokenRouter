package openai

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeriveStableUUIDv4_Deterministic derives a UUID-shaped identity twice
// using the same seed. Both values must match, protecting stable request
// identity across repeated derivations.
func TestDeriveStableUUIDv4_Deterministic(t *testing.T) {
	a := DeriveStableUUIDv4("test-seed-1")
	b := DeriveStableUUIDv4("test-seed-1")
	assert.Equal(t, a, b, "同一种子应返回相同结果")
}

// TestDeriveStableUUIDv4_DifferentSeeds derives identities from two distinct
// fixed seed fixtures. The values must differ for those fixtures, protecting
// separation of their credential namespaces.
func TestDeriveStableUUIDv4_DifferentSeeds(t *testing.T) {
	a := DeriveStableUUIDv4("seed-a")
	b := DeriveStableUUIDv4("seed-b")
	assert.NotEqual(t, a, b, "不同种子应返回不同结果")
}

// TestDeriveStableUUIDv4_ValidFormat parses a derived identifier with the UUID
// library. It verifies accepted syntax, version-four bits, and the RFC4122
// variant used by Codex identity fields.
func TestDeriveStableUUIDv4_ValidFormat(t *testing.T) {
	result := DeriveStableUUIDv4("test-seed")
	parsed, err := uuid.Parse(result)
	require.NoError(t, err, "应返回合法 UUID 格式")
	assert.Equal(t, uuid.Version(4), parsed.Version(), "应为 UUIDv4")
	assert.Equal(t, uuid.RFC4122, parsed.Variant(), "应为 RFC4122 变体")
}
