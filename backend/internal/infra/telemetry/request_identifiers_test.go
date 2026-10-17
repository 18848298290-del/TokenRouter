package telemetry

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRequestIdentifiersSelectOneHeader supplies competing standard and custom
// headers. Only the selected response header becomes an upstream request alias,
// while an absent configured header remains unrecorded.
func TestRequestIdentifiersSelectOneHeader(t *testing.T) {
	for _, tc := range []struct {
		name   string // fixture case label
		header string // selected response header
		want   string // expected diagnostic identifier
	}{
		{"default", "", "standard"},
		{"custom", " X-Client-Request-ID ", "custom"},
		{"missing", "X-Missing-ID", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var saved RequestRecord
			ctx := WithRequestCapture(t.Context(), RequestRecord{RequestID: "local"}, func(r RequestRecord) { saved = r })
			ctx = WithRequestProvider(ctx, 7, tc.header)
			ctx = BeginRequestAttempt(ctx, 1)
			ref := RequestAttemptFromContext(ctx)
			headers := http.Header{"X-Request-Id": {"standard"}, "X-Client-Request-Id": {" custom "}}
			ObserveRequestHeaders(ctx, ref, headers, 200)
			ObserveRequestHeaders(ctx, ref, http.Header{}, 0)
			require.Equal(t, tc.want, saved.Attempts[0].RequestID)
			if tc.want == "" {
				require.Empty(t, saved.Aliases)
			} else {
				require.Equal(t, []RequestAlias{{Kind: "upstream_request", Value: tc.want, Source: ref.Header, AttemptNumber: 1, ProviderID: 7}}, saved.Aliases)
			}
			ObserveResponseID(ctx, ref, "resp_result")
			require.Equal(t, "response", saved.Aliases[len(saved.Aliases)-1].Kind)
			encoded, err := json.Marshal(NormalizeRequestRecord(saved))
			require.NoError(t, err)
			var decoded RequestRecord
			require.NoError(t, json.Unmarshal(encoded, &decoded))
			require.Equal(t, saved.Aliases, decoded.Aliases)
		})
	}
}

// TestRequestIdentifiersKeepDispatchOwnership returns an old response after a
// replacement dispatch. Different and same-provider retries retain separate IDs,
// and a mismatched provider cannot modify the referenced attempt.
func TestRequestIdentifiersKeepDispatchOwnership(t *testing.T) {
	for _, nextProvider := range []int64{7, 8} {
		var saved RequestRecord
		root := WithRequestCapture(t.Context(), RequestRecord{RequestID: "local"}, func(r RequestRecord) { saved = r })
		first := BeginRequestAttempt(WithRequestProvider(root, 7, "X-Client-Request-ID"), 1)
		second := BeginRequestAttempt(WithRequestProvider(root, nextProvider, ""), 2)
		ObserveRequestHeaders(second, RequestAttemptFromContext(second), http.Header{"X-Request-Id": {"second"}}, 200)
		ObserveRequestHeaders(first, RequestAttemptFromContext(first), http.Header{"X-Request-Id": {"unselected"}, "X-Client-Request-Id": {"first"}}, 502)
		wrong := RequestAttemptFromContext(second)
		wrong.ProviderID = 999
		ObserveRequestHeaders(second, wrong, http.Header{"X-Request-Id": {"wrong"}}, 400)
		require.Equal(t, "first", saved.Attempts[0].RequestID)
		require.Equal(t, "second", saved.Attempts[1].RequestID)
		require.Equal(t, 200, saved.Attempts[1].Status)
		require.Len(t, saved.Aliases, 2)
		require.Equal(t, RequestAttemptRef{}, FindRequestAttempt(root, 2, 999))
		require.Equal(t, RequestAttemptRef{}, FindRequestAttempt(root, 2, nextProvider, http.Header{"X-Request-Id": {"first"}}))
		if nextProvider == 7 {
			require.Equal(t, RequestAttemptRef{}, FindRequestAttempt(root, 0, 7))
		}
	}
}

// TestRequestIdentifiersPreserveLegacyAndRejectInvalidValues round-trips ambiguous
// historical aliases and invalid new observations. Prefixes never assign a new
// category, and unrecordable values cannot replace a valid identifier.
func TestRequestIdentifiersPreserveLegacyAndRejectInvalidValues(t *testing.T) {
	var saved RequestRecord
	root := WithRequestCapture(context.Background(), RequestRecord{RequestID: "local", Aliases: []RequestAlias{{Kind: "upstream", Value: "resp_legacy"}}}, func(r RequestRecord) { saved = r })
	ctx := BeginRequestAttempt(WithRequestProvider(root, 7, ""), 0)
	ref := RequestAttemptFromContext(ctx)
	ObserveRequestHeaders(ctx, ref, http.Header{"X-Request-Id": {"invalid\x00id"}}, 200)
	require.Empty(t, saved.Attempts[0].RequestID)
	ObserveRequestHeaders(ctx, ref, http.Header{"X-Request-Id": {"valid"}}, 200)
	ObserveRequestHeaders(ctx, ref, http.Header{"X-Request-Id": {"replacement"}}, 200)
	require.Equal(t, "valid", saved.Attempts[0].RequestID)
	require.Equal(t, RequestAlias{Kind: "upstream", Value: "resp_legacy"}, saved.Aliases[0])
	require.Len(t, saved.Aliases, 2)
}
