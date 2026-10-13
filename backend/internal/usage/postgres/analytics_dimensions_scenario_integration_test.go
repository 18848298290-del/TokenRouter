//go:build integration

package postgres

import (
	"fmt"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/settings/preaggregation"
	"github.com/TokenFlux/TokenRouter/internal/usage"
)

// Legacy usage fields can encode the same request type or model differently
// from current records. Matching fixtures are placed in a raw head, covered
// hourly interval, and raw tail, so a combined query must expose the same
// normalized dimensions at all three positions. Each result is checked against
// its model-specific expectations.

// TestAnalyticsDimensionsAcrossRawAndHourly places four legacy dimension cases
// in raw and hourly source segments. The combined rows agree on payer, absent
// team/group IDs, normalized model/type/mode, platform and endpoint, with each
// case observed three times.
func (s *UsageLogRepoSuite) TestAnalyticsDimensionsAcrossRawAndHourly() {
	user := mustCreateUser(s.T(), s.client, &identity.User{Email: "analytics-dimensions@test.local"})
	key := mustCreateApiKey(s.T(), s.client, &apikey.APIKey{UserID: user.ID, Key: "analytics-dimensions"})
	upstream := mustCreateProvider(s.T(), s.client, &provider.Record{Name: "analytics-dimensions"})
	start := time.Date(2026, 9, 1, 10, 20, 0, 0, time.UTC)
	end := start.Add(200 * time.Minute)
	// Leave the partial first and last hours in raw segments around the covered
	// interval, placing equivalent dimension cases on each side of stored rows.
	coverage := start.Truncate(time.Hour).Add(time.Hour)
	watermark := end.Truncate(time.Hour)
	cases := []struct {
		// model, requested, and wantModel connect raw model representations to
		// the normalized result. A trimmed nonempty model wins; requested supplies
		// the value when model is blank.
		// model is internal; requested is legacy; wantModel is normalized
		model, requested, wantModel string
		// requestType and wantType pair the raw enum with its expected normalized
		// value. Zero lets ws select websocket, then stream select streaming.
		// requestType is stored; wantType is normalized
		requestType, wantType int
		ws, stream            bool // ws marks websocket; stream marks streamed requests
		// video, images, mode, and wantMode describe billing-mode normalization. A
		// nonempty mode wins; with an empty mode, positive video duration takes
		// precedence over positive images.
		video, images  int    // video duration seconds; images generated count
		mode, wantMode string // mode is stored; wantMode is normalized
		// platform snapshot value; endpoint inbound path
		platform, endpoint string
	}{
		{" ", " fallback-ws ", "fallback-ws", 0, 3, true, false, 2, 1, "", "video", "gemini", "/v1/responses"},
		{" internal-stream ", "caller/alias", "internal-stream", 0, 2, false, true, 0, 1, "", "image", "openai", ""},
		{"internal-explicit", "caller/alias", "internal-explicit", 4, 4, false, false, 0, 0, "image", "image", "anthropic", "/v1/messages"},
		{"internal-sync", "caller/alias", "internal-sync", 0, 1, false, false, 0, 0, "", "token", "", ""},
	}
	for caseIndex, tc := range cases {
		for position, at := range []time.Time{start.Add(time.Minute), coverage.Add(time.Minute), watermark.Add(time.Minute)} {
			_, err := s.repo.Create(s.ctx, &usage.UsageLog{
				UserID: user.ID, APIKeyID: key.ID, ProviderID: upstream.ID,
				RequestID: fmt.Sprintf("dimensions-%d-%d", caseIndex, position), Model: "fixture", CreatedAt: at,
			})
			s.Require().NoError(err)
			// Bypass normal insertion normalization after creating each row, exposing
			// legacy blanks and zero request types to both the raw reader and hourly
			// writer.
			_, err = s.tx.ExecContext(s.ctx, `UPDATE usage_logs SET
				model=$1, requested_model=$2, request_type=$3, openai_ws_mode=$4, stream=$5,
				video_duration_seconds=$6, image_count=$7, image_size=CASE WHEN $7>0 THEN '1K' ELSE NULL END, billing_mode=$8, platform=$9, inbound_endpoint=$10,
				team_id=NULL, group_id=NULL, billing_type=0
				WHERE request_id=$11`, tc.model, tc.requested, tc.requestType, tc.ws, tc.stream,
				tc.video, tc.images, tc.mode, tc.platform, tc.endpoint, fmt.Sprintf("dimensions-%d-%d", caseIndex, position))
			s.Require().NoError(err)
		}
	}
	aggregation := NewAggregationStoreWithSQL(s.tx, s.repo.calendar)
	s.Require().NoError(aggregation.AggregateUsageAnalyticsRange(s.ctx, coverage, watermark))
	s.Require().NoError(aggregation.SaveUsageAnalyticsAggregationState(s.ctx, &usage.UsageAnalyticsAggregationState{
		CoverageStart: &coverage, LiveWatermark: watermark, Phase: "idle",
	}))
	s.repo.preAggregation = preaggregation.NewPreAggregationSettingsService(nil, &preaggregation.Options{
		Usage: preaggregation.UsageOptions{Enabled: true},
	})
	query, ok, err := s.repo.buildUsageAnalyticsQuery(s.ctx, UsageLogFilters{}, start, end, false)
	s.Require().NoError(err)
	s.Require().True(ok)
	rows, err := s.tx.QueryContext(s.ctx, query.cte+` SELECT
		user_id,billing_user_id,team_id,api_key_id,group_id,requested_model,request_type,stream,
		billing_type,billing_mode,platform,inbound_endpoint,total_requests FROM combined`, query.args...)
	s.Require().NoError(err)
	// Keep the combined result set available through every dimension assertion,
	// then close it even if a required assertion ends the test early.
	defer func() { _ = rows.Close() }()
	seen := make(map[string]int)
	for rows.Next() {
		var userID, payerID, teamID, keyID, groupID, requests int64
		var model, mode, platform, endpoint string
		var requestType, billingType int
		var stream bool
		s.Require().NoError(rows.Scan(&userID, &payerID, &teamID, &keyID, &groupID, &model, &requestType, &stream, &billingType, &mode, &platform, &endpoint, &requests))
		s.Require().Equal(user.ID, userID)
		s.Require().Equal(user.ID, payerID)
		s.Require().Equal(key.ID, keyID)
		s.Require().Zero(teamID)
		s.Require().Zero(groupID)
		s.Require().Zero(billingType)
		s.Require().Equal(int64(1), requests)
		matched := false
		for _, tc := range cases {
			if tc.wantModel != model {
				continue
			}
			matched = true
			s.Require().Equal(tc.wantType, requestType)
			s.Require().Equal(tc.stream, stream)
			s.Require().Equal(tc.wantMode, mode)
			s.Require().Equal(tc.platform, platform)
			s.Require().Equal(tc.endpoint, endpoint)
			seen[model]++
		}
		s.Require().True(matched, "未预期的模型：%s", model)
	}
	s.Require().NoError(rows.Err())
	for _, tc := range cases {
		s.Require().Equal(3, seen[tc.wantModel], tc.wantModel)
	}
}
