package ops

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Group percentage and provider predicates use supplied availability
// observations, including error entries with temporary-unschedulable deadlines.
// These tests check selected indicator values and missing group identity using
// immutable fixtures shared by parallel subtests; they do not enter evaluator
// lifecycle or event persistence.

// The interface assertion checks the dashboard stub's repository method set,
// including methods promoted from its embedded interface for compilation.
var _ OpsRepository = (*stubOpsRepo)(nil)

// stubOpsRepo supplies dashboard results while other repository methods remain
// promoted from its embedded interface.
type stubOpsRepo struct {
	// OpsRepository holds promoted methods that these selected metric
	// fixtures leave nil. Tests keep their calls on the overridden or
	// independent metric paths.
	OpsRepository
	// overview is returned by reference when present. Consumers share the
	// fixture pointer and coordinate any mutations while parallel readers
	// use it.
	// overview and err select the dashboard response: err takes precedence,
	// overview is returned when supplied, and absence of both allocates a
	// new empty result.
	overview *OpsDashboardOverview // supplied dashboard result
	err      error                 // forced dashboard read failure
}

// GetDashboardOverview returns the configured error before inspecting its
// supplied overview pointer. Context and filter are ignored; a present overview
// is shared, while an absent overview allocates a new empty value.
func (s *stubOpsRepo) GetDashboardOverview(ctx context.Context, filter *OpsDashboardFilter) (*OpsDashboardOverview, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.overview != nil {
		return s.overview, nil
	}
	return &OpsDashboardOverview{}, nil
}

// TestComputeGroupAvailableRatio supplies normal, zero-total, and
// zero-available group counts in parallel cases. It checks eighty percent for
// eight of ten and zero for the two supplied empty-count cases.
func TestComputeGroupAvailableRatio(t *testing.T) {
	t.Parallel()

	t.Run("正常情况: 10个提供商, 8个可用 = 80%", func(t *testing.T) {
		t.Parallel()

		got := computeGroupAvailableRatio(&GroupAvailability{
			TotalProviders: 10,
			AvailableCount: 8,
		})
		// Allow a small absolute tolerance for percentage arithmetic.
		require.InDelta(t, 80.0, got, 0.0001)
	})

	t.Run("边界情况: TotalProviders = 0 应返回 0", func(t *testing.T) {
		t.Parallel()

		got := computeGroupAvailableRatio(&GroupAvailability{
			TotalProviders: 0,
			AvailableCount: 8,
		})
		require.Equal(t, 0.0, got)
	})

	t.Run("边界情况: AvailableCount = 0 应返回 0%", func(t *testing.T) {
		t.Parallel()

		got := computeGroupAvailableRatio(&GroupAvailability{
			TotalProviders: 10,
			AvailableCount: 0,
		})
		require.Equal(t, 0.0, got)
	})
}

// TestCountProvidersByCondition supplies rate-limited providers, error
// providers with a temporary exclusion, and an empty map. Parallel cases check
// each selected predicate's count against its supplied fixture.
func TestCountProvidersByCondition(t *testing.T) {
	t.Parallel()

	t.Run("测试限流提供商统计: acc.IsRateLimited", func(t *testing.T) {
		t.Parallel()

		providers := map[int64]*ProviderAvailability{
			1: {IsRateLimited: true},
			2: {IsRateLimited: false},
			3: {IsRateLimited: true},
		}

		got := countProvidersByCondition(providers, func(acc *ProviderAvailability) bool {
			return acc.IsRateLimited
		})
		require.Equal(t, int64(2), got)
	})

	t.Run("测试错误提供商统计（排除临时不可调度）: acc.HasError && acc.TempUnschedulableUntil == nil", func(t *testing.T) {
		t.Parallel()

		until := time.Now().UTC().Add(5 * time.Minute)
		providers := map[int64]*ProviderAvailability{
			1: {HasError: true},
			2: {HasError: true, TempUnschedulableUntil: &until},
			3: {HasError: false},
		}

		got := countProvidersByCondition(providers, func(acc *ProviderAvailability) bool {
			return acc.HasError && acc.TempUnschedulableUntil == nil
		})
		require.Equal(t, int64(1), got)
	})

	t.Run("边界情况: 空 map 应返回 0", func(t *testing.T) {
		t.Parallel()

		got := countProvidersByCondition(map[int64]*ProviderAvailability{}, func(acc *ProviderAvailability) bool {
			return acc.IsRateLimited
		})
		require.Equal(t, int64(0), got)
	})
}

// TestComputeRuleMetricNewIndicators supplies disk and availability
// observations to selected direct indicator branches. Parallel cases check
// returned values and usability, including unavailable group metrics when the
// group identity is absent.
func TestComputeRuleMetricNewIndicators(t *testing.T) {
	t.Parallel()

	groupID := int64(101)
	platform := "openai"

	availability := &OpsProviderAvailability{
		Group: &GroupAvailability{
			GroupID:        groupID,
			TotalProviders: 10,
			AvailableCount: 8,
		},
		Providers: map[int64]*ProviderAvailability{
			1: {IsRateLimited: true},
			2: {IsRateLimited: true},
			3: {HasError: true},
			4: {HasError: true, TempUnschedulableUntil: timePtr(time.Now().UTC().Add(2 * time.Minute))},
			5: {HasError: false, IsRateLimited: false},
		},
	}

	// Parallel cases share these availability observations as read-only
	// data; the injected getter bypasses production provider queries and
	// returns the same fixture pointer.
	opsService := &OpsService{
		getProviderAvailability: func(_ context.Context, _ string, _ *int64) (*OpsProviderAvailability, error) {
			return availability, nil
		},
	}

	svc := &OpsAlertEvaluatorService{
		opsService: opsService,
		opsRepo:    &stubOpsRepo{overview: &OpsDashboardOverview{}},
	}

	start := time.Now().UTC().Add(-5 * time.Minute)
	end := time.Now().UTC()
	ctx := context.Background()

	tests := []struct {
		name       string // subtest display name
		metricType string // selected indicator kind
		// system supplies sample-based indicators and is read by
		// computeRuleMetric. Parallel cases keep the pointed fixture
		// values unchanged throughout their evaluation.
		system *OpsSystemMetricsSnapshot // system sample pointer
		// groupID is passed unchanged to metric selection. Nil
		// exercises unavailable group observations; the supplied group
		// cases use the positive fixture identity.
		groupID *int64 // optional group selector
		// wantValue and wantOK describe usable observations together. A
		// false wantOK ends the case after checking availability, so
		// its numeric result is not asserted.
		wantValue float64 // expected usable value
		wantOK    bool    // expected usable flag
	}{
		{
			name:       "disk_usage_percent",
			metricType: "disk_usage_percent",
			system: &OpsSystemMetricsSnapshot{
				DiskUsagePercent: float64Ptr(92.5),
			},
			groupID:   nil,
			wantValue: 92.5,
			wantOK:    true,
		},
		{
			name:       "group_available_providers",
			metricType: "group_available_providers",
			groupID:    &groupID,
			wantValue:  8,
			wantOK:     true,
		},
		{
			name:       "group_available_ratio",
			metricType: "group_available_ratio",
			groupID:    &groupID,
			wantValue:  80.0,
			wantOK:     true,
		},
		{
			name:       "provider_rate_limited_count",
			metricType: "provider_rate_limited_count",
			groupID:    nil,
			wantValue:  2,
			wantOK:     true,
		},
		{
			name:       "provider_error_count",
			metricType: "provider_error_count",
			groupID:    nil,
			wantValue:  1,
			wantOK:     true,
		},
		{
			name:       "group_available_providers without group_id returns false",
			metricType: "group_available_providers",
			groupID:    nil,
			wantValue:  0,
			wantOK:     false,
		},
		{
			name:       "group_available_ratio without group_id returns false",
			metricType: "group_available_ratio",
			groupID:    nil,
			wantValue:  0,
			wantOK:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rule := &OpsAlertRule{
				MetricType: tt.metricType,
			}
			gotValue, gotOK := svc.computeRuleMetric(ctx, rule, tt.system, start, end, platform, tt.groupID)
			require.Equal(t, tt.wantOK, gotOK)
			if !tt.wantOK {
				return
			}
			// Use a small absolute tolerance for returned
			// measurements.
			require.InDelta(t, tt.wantValue, gotValue, 0.0001)
		})
	}
}
