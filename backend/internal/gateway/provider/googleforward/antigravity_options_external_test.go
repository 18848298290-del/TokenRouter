package googleforward_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/provider/googleforward"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/antigravity"
)

// TestAntigravityUpstreamErrorBodyReadLimit_RespectsDiagnosticLimit configures
// a logging cap larger than the standard error inspection cap. The selected
// read limit equals that diagnostic cap; the test inspects selection without
// reading a response.
func TestAntigravityUpstreamErrorBodyReadLimit_RespectsDiagnosticLimit(t *testing.T) {
	svc := newAntigravityStreamFixture(&googleforward.Options{LogErrorBody: true, LogErrorBodyMaxBytes: int(int64(512<<10)) + 1024})

	require.Equal(t, int64(svc.Options.LogErrorBodyMaxBytes), googleforward.ErrorBodyLimitForTest(svc))
}

// TestResolveAntigravityProjectID compares automatic, credential fallback,
// legacy extra fallback, and missing project values. The resolver returns the
// trimmed selected project or the missing-project sentinel for the exercised
// inputs.
func TestResolveAntigravityProjectID(t *testing.T) {
	tests := []struct {
		name string // table scenario name
		// provider project fixture value
		provider *gatewayprovider.ExecutionProvider
		want     string // expected resolved project identifier
		wantErr  bool   // expected missing-project error flag
	}{
		{
			name: "优先使用自动回填的 project_id",

			provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{
				LoadLocation: time.LoadLocation,
				Credentials: map[string]any{
					"project_id":             " onboard-project ",
					"antigravity_project_id": " configured-project ",
				},
			}},

			want: "onboard-project",
		},

		{
			name: "使用 credentials 中的手工 fallback",

			provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{
				LoadLocation: time.LoadLocation,
				Credentials: map[string]any{
					"antigravity_project_id": " configured-project ",
				},
			}},

			want: "configured-project",
		},

		{
			name: "兼容 extra 中的手工 fallback",

			provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Extra: map[string]any{
				"antigravity_project_id": " extra-project ",
			}}},

			want: "extra-project",
		},

		{
			name: "缺少 project_id",

			provider: &gatewayprovider.ExecutionProvider{Record: providercore.Record{LoadLocation: time.LoadLocation, Credentials: map[string]any{}}},

			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := googleforward.ResolveProjectForTest(tc.provider)
			if tc.wantErr {
				require.ErrorIs(t, err, antigravity.ErrProjectIDRequired)
				require.Empty(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
