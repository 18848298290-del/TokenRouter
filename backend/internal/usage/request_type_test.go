package usage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Stored usage request categories have text labels and older transport-flag
// representations. These cases check the supplied parse spellings, invalid
// numeric normalization and the chosen legacy mappings, including
// special-category inputs whose booleans need to remain available.

// TestParseUsageRequestType checks the supplied category labels, uppercase
// input, surrounding spaces and unsupported text. It requires the expected
// parsed category or an error for each fixture.
func TestParseUsageRequestType(t *testing.T) {
	t.Parallel()

	// testCase pairs request category text with its expected parsing outcome.
	type testCase struct {
		name    string      // subtest case label
		input   string      // input category text
		want    RequestType // expected request category
		wantErr bool        // expected parsing failure flag
	}

	cases := []testCase{
		{name: "unknown", input: "unknown", want: RequestTypeUnknown},
		{name: "sync", input: "sync", want: RequestTypeSync},
		{name: "stream", input: "stream", want: RequestTypeStream},
		{name: "ws_v2", input: "ws_v2", want: RequestTypeWSV2},
		{name: "cyber", input: "cyber", want: RequestTypeCyberBlocked},
		{name: "case_insensitive", input: "WS_V2", want: RequestTypeWSV2},
		{name: "trim_spaces", input: "  stream  ", want: RequestTypeStream},
		{name: "invalid", input: "xxx", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseUsageRequestType(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestRequestTypeNormalizeAndString supplies an unsupported numeric code and
// the tested known categories. It requires unknown normalization and the
// expected lowercase display labels.
func TestRequestTypeNormalizeAndString(t *testing.T) {
	t.Parallel()

	require.Equal(t, RequestTypeUnknown, RequestType(99).Normalize())
	require.Equal(t, "unknown", RequestType(99).String())
	require.Equal(t, "sync", RequestTypeSync.String())
	require.Equal(t, "stream", RequestTypeStream.String())
	require.Equal(t, "ws_v2", RequestTypeWSV2.String())
	require.Equal(t, "cyber", RequestTypeCyberBlocked.String())
}

// TestRequestTypeFromLegacy supplies the three selected stream and WebSocket
// flag combinations. It requires WebSocket precedence and the corresponding
// stream or synchronous category.
func TestRequestTypeFromLegacy(t *testing.T) {
	t.Parallel()

	require.Equal(t, RequestTypeWSV2, RequestTypeFromLegacy(false, true))
	require.Equal(t, RequestTypeStream, RequestTypeFromLegacy(true, false))
	require.Equal(t, RequestTypeSync, RequestTypeFromLegacy(false, false))
}

// TestApplyLegacyRequestFields supplies transport categories plus cyber and
// unknown fixtures. It checks rebuilt transport flags and retention of
// supplied booleans for the two special fallback cases.
func TestApplyLegacyRequestFields(t *testing.T) {
	t.Parallel()

	stream, ws := ApplyLegacyRequestFields(RequestTypeSync, true, true)
	require.False(t, stream)
	require.False(t, ws)

	stream, ws = ApplyLegacyRequestFields(RequestTypeStream, false, true)
	require.True(t, stream)
	require.False(t, ws)

	stream, ws = ApplyLegacyRequestFields(RequestTypeWSV2, false, false)
	require.True(t, stream)
	require.True(t, ws)

	stream, ws = ApplyLegacyRequestFields(RequestTypeCyberBlocked, true, true)
	require.True(t, stream)
	require.True(t, ws)

	stream, ws = ApplyLegacyRequestFields(RequestTypeUnknown, true, false)
	require.True(t, stream)
	require.False(t, ws)
}
