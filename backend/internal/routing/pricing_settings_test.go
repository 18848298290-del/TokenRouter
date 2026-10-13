package routing

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// Shared billing settings apply to every associated group even when a
// configuration has no model cards. A single-record repository clones its
// stored value so the CRUD scenario can check optional free prices, cleared
// overrides, reassigned groups, and inactive configuration defaults through
// later cache reads.

// settingsRepository stores one cloned configuration for sequential
// shared-settings CRUD and cache tests.
type settingsRepository struct {
	// PricingConfigRepository provides unused methods through an embedded nil
	// interface; these fixtures implement the service paths exercised by the
	// settings tests.
	PricingConfigRepository // unused repository method placeholders
	// value holds a Clone made during Create or Update. Read methods clone it
	// again, and Delete clears it to represent absent configuration.
	value *PricingConfig // stored single configuration snapshot
}

// settingsInvalidator records the group IDs passed to authentication-cache
// invalidation.
type settingsInvalidator struct {
	groups []int64 // recorded group invalidation requests
}

// TestSharedBillingSettingsCRUDAndCache creates settings shared by two
// groups, mutates copied prices, clears an override, and changes membership
// and status. Later reads retain stored values and restore defaults for
// departed, disabled, or deleted associations.
func TestSharedBillingSettingsCRUDAndCache(t *testing.T) {
	ctx := context.Background()
	repo := &settingsRepository{}
	invalidator := &settingsInvalidator{}
	svc := NewPricingConfigService(repo, invalidator)
	created, err := svc.Create(ctx, &CreatePricingConfigInput{Name: "shared", GroupIDs: []int64{7, 8}})
	require.NoError(t, err)
	require.Equal(t, pricing.DefaultBillingSettings(), created.BillingSettings)
	require.ElementsMatch(t, []int64{7, 8}, invalidator.groups)
	// Apply settings to a configuration with no model cards so the following
	// reads demonstrate that card matching is independent of shared billing
	// switches and explicitly supplied free operation prices.
	zero, enabled, disabled := 0.0, true, false
	_, err = svc.Update(ctx, created.ID, &UpdatePricingConfigInput{BillingSettingsPatch: BillingSettingsPatch{
		FreeOpenAIFast: &enabled, LongContextPricingEnabled: &disabled, WebSearchPricePerCall: PriceUpdate{Set: true, Value: &zero},
	}})
	require.NoError(t, err)
	for _, id := range []int64{7, 8} {
		got := svc.GetEffectiveBillingSettings(ctx, id)
		require.True(t, got.FreeOpenAIFast)
		require.False(t, got.LongContextPricingEnabled)
		require.NotNil(t, got.WebSearchPricePerCall)
		require.Zero(t, *got.WebSearchPricePerCall)
		*got.WebSearchPricePerCall = 99
	}
	require.Zero(t, *svc.GetEffectiveBillingSettings(ctx, 7).WebSearchPricePerCall)
	_, err = svc.Update(ctx, created.ID, &UpdatePricingConfigInput{BillingSettingsPatch: BillingSettingsPatch{WebSearchPricePerCall: PriceUpdate{Set: true}}})
	require.NoError(t, err)
	require.Nil(t, svc.GetEffectiveBillingSettings(ctx, 7).WebSearchPricePerCall)
	require.True(t, svc.GetEffectiveBillingSettings(ctx, 7).FreeOpenAIFast)
	// Reset recorded invalidations before replacing membership so the assertion
	// can identify exactly the departed, retained, and newly associated groups
	// affected by that update.
	invalidator.groups = nil
	members := []int64{8, 9}
	_, err = svc.Update(ctx, created.ID, &UpdatePricingConfigInput{GroupIDs: &members})
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{7, 8, 9}, invalidator.groups)
	require.Equal(t, pricing.DefaultBillingSettings(), svc.GetEffectiveBillingSettings(ctx, 7))
	require.True(t, svc.GetEffectiveBillingSettings(ctx, 9).FreeOpenAIFast)
	_, err = svc.Update(ctx, created.ID, &UpdatePricingConfigInput{Status: StatusDisabled})
	require.NoError(t, err)
	require.Equal(t, pricing.DefaultBillingSettings(), svc.GetEffectiveBillingSettings(ctx, 9))
	require.NoError(t, svc.Delete(ctx, created.ID))
	require.Equal(t, pricing.DefaultBillingSettings(), svc.GetEffectiveBillingSettings(ctx, 8))
}

// TestBillingSettingsValidateMergedInput applies partial patches to default
// or previously changed settings. It checks merged peak-window validation,
// discount versus hold ordering, acceptance of zero peak cost, and rejection
// of a negative optional price.
func TestBillingSettingsValidateMergedInput(t *testing.T) {
	s := pricing.DefaultBillingSettings()
	enabled, start, end := true, "14:00", "18:00"
	peak, discount, hold := 0.0, 0.8, 0.9
	require.NoError(t, (BillingSettingsPatch{PeakRateEnabled: &enabled, PeakStart: &start, PeakEnd: &end, PeakRateMultiplier: &peak, BatchImageDiscountMultiplier: &discount, BatchImageHoldMultiplier: &hold}).Apply(&s))
	badEnd := "13:00"
	require.Error(t, (BillingSettingsPatch{PeakEnd: &badEnd}).Apply(&s))
	s = pricing.DefaultBillingSettings()
	require.Error(t, (BillingSettingsPatch{BatchImageDiscountMultiplier: &discount}).Apply(&s))
	negative := -1.0
	require.Error(t, (BillingSettingsPatch{SearchPricePer1k: PriceUpdate{Set: true, Value: &negative}}).Apply(&s))
}

// TestValidatePeakRateConfig supplies enabled and disabled windows with
// valid, malformed, missing, equal, and wrapping clocks. Assertions also
// distinguish a rejected negative multiplier from an accepted explicit zero.
func TestValidatePeakRateConfig(t *testing.T) {
	cases := []struct {
		name    string  // subtest validation case label
		enabled bool    // peak window enable flag
		start   string  // configured window start clock
		end     string  // configured window end clock
		mult    float64 // configured peak scaling factor
		wantErr bool    // expected validation error presence
	}{
		{"disabled passes through", false, "", "", 0, false},
		{"enabled valid", true, "14:00", "18:00", 3.0, false},
		{"enabled valid single digit hour", true, "1:00", "2:00", 3.0, false},
		{"enabled empty start", true, "", "18:00", 1.0, true},
		{"enabled empty end", true, "14:00", "", 1.0, true},
		{"enabled malformed start", true, "99:99", "18:00", 1.0, true},
		{"enabled malformed end", true, "14:00", "25:00", 1.0, true},
		{"enabled equal start==end", true, "14:00", "14:00", 1.0, true},
		{"enabled cross-day rejected", true, "22:00", "02:00", 1.0, true},
		{"enabled negative multiplier", true, "14:00", "18:00", -0.5, true},
		{"enabled zero multiplier allowed", true, "14:00", "18:00", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidatePeakRateConfig(c.enabled, c.start, c.end, c.mult)
			if c.wantErr && err == nil {
				t.Fatalf("expect error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("expect no error, got %v", err)
			}
		})
	}
}

// TestParseMinutesMatchesLegacyTimeParseShape exercises single and
// double-digit hours, fixed-width minutes, limits, and surrounding
// whitespace. Each case checks parse success, and successful cases check
// minutes since midnight.
func TestParseMinutesMatchesLegacyTimeParseShape(t *testing.T) {
	cases := []struct {
		value string // supplied clock input text
		want  int    // expected minutes since midnight
		ok    bool   // expected clock parse success
	}{
		{"0:00", 0, true},
		{"00:00", 0, true},
		{"1:30", 90, true},
		{"01:30", 90, true},
		{"23:59", 1439, true},
		{"001:30", 0, false},
		{"9:3", 0, false},
		{"24:00", 0, false},
		{"1:030", 0, false},
		{" 1:30", 0, false},
		{"1:30 ", 0, false},
	}

	for _, c := range cases {
		t.Run(c.value, func(t *testing.T) {
			got, ok := ParseMinutes(c.value)
			if ok != c.ok {
				t.Fatalf("ok: got %v, want %v", ok, c.ok)
			}
			if ok && got != c.want {
				t.Fatalf("minutes: got %v, want %v", got, c.want)
			}
		})
	}
}

// TestNormalizePeakRateConfig supplies disabled invalid settings, disabled
// valid settings, and enabled invalid settings. It checks cleanup for
// disabled input and retention of enabled input for later validation.
func TestNormalizePeakRateConfig(t *testing.T) {
	enabled, start, end, multiplier := NormalizePeakRateConfig(false, "bad", "18:00", -2)
	if enabled || start != "" || end != "18:00" || multiplier != 1.0 {
		t.Fatalf("disabled cleanup mismatch: enabled=%v start=%q end=%q multiplier=%v", enabled, start, end, multiplier)
	}

	enabled, start, end, multiplier = NormalizePeakRateConfig(false, "14:00", "18:00", 3)
	if enabled || start != "14:00" || end != "18:00" || multiplier != 3 {
		t.Fatalf("disabled valid config should be preserved: enabled=%v start=%q end=%q multiplier=%v", enabled, start, end, multiplier)
	}

	enabled, start, end, multiplier = NormalizePeakRateConfig(true, "bad", "18:00", -2)
	if !enabled || start != "bad" || end != "18:00" || multiplier != -2 {
		t.Fatalf("enabled config should be left for validation: enabled=%v start=%q end=%q multiplier=%v", enabled, start, end, multiplier)
	}
}

// ExistsByName reports no name conflict for the single-record settings
// fixture.
func (r *settingsRepository) ExistsByName(context.Context, string) (bool, error) { return false, nil }

// GetGroupsInOtherPricingConfigs reports no competing memberships for the
// single-record fixture.
func (r *settingsRepository) GetGroupsInOtherPricingConfigs(context.Context, int64, []int64) ([]int64, error) {
	return nil, nil
}

// Create assigns configuration ID 1 and stores a Clone of the supplied value.
// Callers can inspect the assigned ID while later input mutations remain
// separate from stored fields.
func (r *settingsRepository) Create(_ context.Context, c *PricingConfig) error {
	c.ID = 1
	r.value = c.Clone()
	return nil
}

// GetByID returns a Clone of the stored value regardless of the requested ID.
// After deletion, the nil value produces a nil result with no error.
func (r *settingsRepository) GetByID(context.Context, int64) (*PricingConfig, error) {
	return r.value.Clone(), nil
}

// Update replaces the stored value with a Clone of the supplied
// configuration. It accepts every request, allowing the scenario's later
// service reload to observe its updated fields.
func (r *settingsRepository) Update(_ context.Context, c *PricingConfig) error {
	r.value = c.Clone()
	return nil
}

// ListAll returns a cloned single-row slice when a configuration is stored.
// After Delete or before Create it returns nil, allowing cache rebuilds to
// represent absent settings.
func (r *settingsRepository) ListAll(context.Context) ([]PricingConfig, error) {
	if r.value == nil {
		return nil, nil
	}
	return []PricingConfig{*r.value.Clone()}, nil
}

// GetGroupIDs returns a copied membership slice from the stored
// configuration. Call it while value exists; an empty membership produces nil
// and callers can edit returned IDs independently.
func (r *settingsRepository) GetGroupIDs(context.Context, int64) ([]int64, error) {
	return append([]int64(nil), r.value.GroupIDs...), nil
}

// Delete clears the fixture's stored configuration and succeeds for any
// requested ID.
func (r *settingsRepository) Delete(context.Context, int64) error { r.value = nil; return nil }

// InvalidateAuthCacheByGroupID appends each group request to the fixture's
// observed IDs.
func (i *settingsInvalidator) InvalidateAuthCacheByGroupID(_ context.Context, id int64) {
	i.groups = append(i.groups, id)
}
