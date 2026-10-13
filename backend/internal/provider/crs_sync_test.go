package provider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// TestBuildSelectedSet supplies absent, empty, populated, and duplicate
// source-ID selections. It asserts nil and nonnil empty maps retain distinct
// creation meaning and every unique supplied ID appears in the result.
func TestBuildSelectedSet(t *testing.T) {
	tests := []struct {
		name     string   // table case display name
		ids      []string // supplied source identity selection
		wantNil  bool     // expect absent selection sentinel
		wantSize int      // expected unique membership count
	}{
		{
			name:    "nil input returns nil (backward compatible: create all)",
			ids:     nil,
			wantNil: true,
		},
		{
			name:     "empty slice returns empty map (create none)",
			ids:      []string{},
			wantNil:  false,
			wantSize: 0,
		},
		{
			name:     "single ID",
			ids:      []string{"abc-123"},
			wantNil:  false,
			wantSize: 1,
		},
		{
			name:     "multiple IDs",
			ids:      []string{"a", "b", "c"},
			wantNil:  false,
			wantSize: 3,
		},
		{
			name:     "duplicate IDs are deduplicated",
			ids:      []string{"a", "a", "b"},
			wantNil:  false,
			wantSize: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CRSBuildSelectedSet(tt.ids)
			if tt.wantNil {
				if got != nil {
					t.Errorf("buildSelectedSet(%v) = %v, want nil", tt.ids, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("buildSelectedSet(%v) = nil, want non-nil map", tt.ids)
			}
			if len(got) != tt.wantSize {
				t.Errorf("buildSelectedSet(%v) has %d entries, want %d", tt.ids, len(got), tt.wantSize)
			}
			// Every distinct input ID appears in the result.
			for _, id := range tt.ids {
				if _, ok := got[id]; !ok {
					t.Errorf("buildSelectedSet(%v) missing key %q", tt.ids, id)
				}
			}
		})
	}
}

// TestShouldCreateProvider checks exact ID membership under absent, empty, and
// populated creation selections. It asserts nil admits every ID, explicit empty
// admits none, and unmatched IDs are rejected.
func TestShouldCreateProvider(t *testing.T) {
	tests := []struct {
		name  string // table case display name
		crsID string // queried source account identity
		// selectedSet chooses absent, explicit empty, or populated
		// membership
		// fixtures; each case exercises a distinct creation choice
		// returned by
		// CRSShouldCreateProvider.
		selectedSet map[string]struct{}
		want        bool // expected new creation eligibility
	}{
		{
			name:        "nil set allows all (backward compatible)",
			crsID:       "any-id",
			selectedSet: nil,
			want:        true,
		},
		{
			name:        "empty set blocks all",
			crsID:       "any-id",
			selectedSet: map[string]struct{}{},
			want:        false,
		},
		{
			name:        "ID in set is allowed",
			crsID:       "abc-123",
			selectedSet: map[string]struct{}{"abc-123": {}, "def-456": {}},
			want:        true,
		},
		{
			name:        "ID not in set is blocked",
			crsID:       "xyz-789",
			selectedSet: map[string]struct{}{"abc-123": {}, "def-456": {}},
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CRSShouldCreateProvider(tt.crsID, tt.selectedSet)
			if got != tt.want {
				t.Errorf("shouldCreateProvider(%q, %v) = %v, want %v",
					tt.crsID, tt.selectedSet, got, tt.want)
			}
		})
	}
}

// TestGuardCRSShadowParentInvariant adds a Spark shadow to an OpenAI OAuth
// parent after checking unrestricted target conversion. It asserts incompatible
// platform/type targets are rejected while OpenAI OAuth remains accepted; this
// exercises the guard directly.
func TestGuardCRSShadowParentInvariant(t *testing.T) {
	ctx := context.Background()
	repo := newCRSShadowStore()

	mother := &Record{Platform: capability.PlatformOpenAI, Type: capability.ProviderTypeOAuth}
	require.NoError(t, repo.Create(ctx, mother))
	parentID := mother.ID

	// Without shadows, platform and credential type may change.
	require.NoError(t, GuardCRSShadowParentInvariant(ctx, repo, mother, capability.PlatformOpenAI, capability.ProviderTypeAPIKey))
	require.NoError(t, GuardCRSShadowParentInvariant(ctx, repo, mother, capability.PlatformAnthropic, capability.ProviderTypeOAuth))

	// Add a shadow before checking the restrictions.
	shadow := &Record{
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		ParentProviderID: &parentID,
		QuotaDimension:   QuotaDimensionSpark,
	}
	require.NoError(t, repo.Create(ctx, shadow))

	// Reject conversion to an OpenAI API Key provider.
	err := GuardCRSShadowParentInvariant(ctx, repo, mother, capability.PlatformOpenAI, capability.ProviderTypeAPIKey)
	require.Error(t, err, "must reject converting a shadow parent to openai api_key")
	require.Contains(t, err.Error(), "spark-shadow parent")

	// Reject an Anthropic target even when its type remains OAuth.
	require.Error(t, GuardCRSShadowParentInvariant(ctx, repo, mother, capability.PlatformAnthropic, capability.ProviderTypeOAuth),
		"must reject moving a shadow parent to a non-OpenAI platform even if type stays oauth")

	// Reject a Gemini API Key target.
	require.Error(t, GuardCRSShadowParentInvariant(ctx, repo, mother, capability.PlatformGemini, capability.ProviderTypeAPIKey))

	// Allow OpenAI OAuth resynchronization while shadows exist.
	require.NoError(t, GuardCRSShadowParentInvariant(ctx, repo, mother, capability.PlatformOpenAI, capability.ProviderTypeOAuth))
}
