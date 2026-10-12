package routing

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing/pricing"
)

// settingsRepository stores pricing configurations and group associations for settings tests.
type settingsRepository struct {
	PricingConfigRepository
	value *PricingConfig
}

type settingsInvalidator struct{ groups []int64 }

func TestSharedBillingSettingsCRUDAndCache(t *testing.T) {
	ctx := context.Background()
	repo := &settingsRepository{}
	invalidator := &settingsInvalidator{}
	svc := NewPricingConfigService(repo, invalidator)
	created, err := svc.Create(ctx, &CreatePricingConfigInput{Name: "shared", GroupIDs: []int64{7, 8}})
	require.NoError(t, err)
	require.Equal(t, pricing.DefaultBillingSettings(), created.BillingSettings)
	require.ElementsMatch(t, []int64{7, 8}, invalidator.groups)
	// Configuration-level switches and zero prices apply even when there are no model entries.
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

func TestValidatePeakRateConfig(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		start   string
		end     string
		mult    float64
		wantErr bool
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

func TestParseMinutesMatchesLegacyTimeParseShape(t *testing.T) {
	cases := []struct {
		value string
		want  int
		ok    bool
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

func (r *settingsRepository) ExistsByName(context.Context, string) (bool, error) { return false, nil }

func (r *settingsRepository) GetGroupsInOtherPricingConfigs(context.Context, int64, []int64) ([]int64, error) {
	return nil, nil
}

func (r *settingsRepository) Create(_ context.Context, c *PricingConfig) error {
	c.ID = 1
	r.value = c.Clone()
	return nil
}

func (r *settingsRepository) GetByID(context.Context, int64) (*PricingConfig, error) {
	return r.value.Clone(), nil
}

func (r *settingsRepository) Update(_ context.Context, c *PricingConfig) error {
	r.value = c.Clone()
	return nil
}

func (r *settingsRepository) ListAll(context.Context) ([]PricingConfig, error) {
	if r.value == nil {
		return nil, nil
	}
	return []PricingConfig{*r.value.Clone()}, nil
}

func (r *settingsRepository) GetGroupIDs(context.Context, int64) ([]int64, error) {
	return append([]int64(nil), r.value.GroupIDs...), nil
}

func (r *settingsRepository) Delete(context.Context, int64) error { r.value = nil; return nil }

func (i *settingsInvalidator) InvalidateAuthCacheByGroupID(_ context.Context, id int64) {
	i.groups = append(i.groups, id)
}
