package ops

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// TestDashboardQueryValidationOrder sends absent dependencies and invalid
// ranges through six dashboard query facades. It checks
// monitoring/repository/filter/time error precedence, leaves supplied filter
// values unchanged on failure, and prevents access to the unconfigured embedded
// repository.
func TestDashboardQueryValidationOrder(t *testing.T) {
	queries := map[string]func(*OpsService, *OpsDashboardFilter) error{
		"overview": func(s *OpsService, f *OpsDashboardFilter) error {
			_, err := s.GetDashboardOverview(t.Context(), f)
			return err
		},
		"latency": func(s *OpsService, f *OpsDashboardFilter) error {
			_, err := s.GetLatencyHistogram(t.Context(), f, nil)
			return err
		},
		"realtime": func(s *OpsService, f *OpsDashboardFilter) error {
			_, err := s.GetRealtimeTrafficSummary(t.Context(), f)
			return err
		},
		"throughput": func(s *OpsService, f *OpsDashboardFilter) error {
			_, err := s.GetThroughputTrend(t.Context(), f, 60)
			return err
		},
		"errors": func(s *OpsService, f *OpsDashboardFilter) error {
			_, err := s.GetErrorTrend(t.Context(), f, 60)
			return err
		},
		"distribution": func(s *OpsService, f *OpsDashboardFilter) error {
			_, err := s.GetErrorDistribution(t.Context(), f)
			return err
		},
	}
	now := time.Now()
	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			// The repository fixture deliberately leaves methods unconfigured. A storage
			// call would panic, making early validation observable across each facade
			// without a working database query.
			repo := struct {
				OpsRepository /* deliberately unconfigured repository methods */
			}{}
			for _, tc := range []struct {
				name    string              // validation case display name
				service *OpsService         // service dependency configuration fixture
				filter  *OpsDashboardFilter // submitted invalid range fixture
				reason  string              // expected application error reason
			}{
				{name: "disabled", service: &OpsService{cfg: &Options{}}, reason: apperror.Reason(ErrOpsDisabled)},
				{name: "repository", service: &OpsService{}, reason: "OPS_REPO_UNAVAILABLE"},
				{name: "filter", service: &OpsService{opsRepo: repo}, reason: "OPS_FILTER_REQUIRED"},
				{name: "start", service: &OpsService{opsRepo: repo}, filter: &OpsDashboardFilter{EndTime: now}, reason: "OPS_TIME_RANGE_REQUIRED"},
				{name: "end", service: &OpsService{opsRepo: repo}, filter: &OpsDashboardFilter{StartTime: now}, reason: "OPS_TIME_RANGE_REQUIRED"},
				{name: "reversed", service: &OpsService{opsRepo: repo}, filter: &OpsDashboardFilter{StartTime: now, EndTime: now.Add(-time.Second)}, reason: "OPS_TIME_RANGE_INVALID"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var before OpsDashboardFilter
					if tc.filter != nil {
						before = *tc.filter
					}
					err := query(tc.service, tc.filter)
					require.Error(t, err)
					require.Equal(t, tc.reason, apperror.Reason(err))
					if tc.filter != nil {
						require.Equal(t, before, *tc.filter)
					}
				})
			}
		})
	}
}
