package ops

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/server/httpx"
)

// tokenStatsRepoStub captures token-stat filters and supplies configured
// response or error results.
type tokenStatsRepoStub struct {
	// OpsRepository leaves unrelated methods unconfigured through its nil
	// embedded interface. These tests invoke the GetTokenStats override and need
	// another implementation for other calls.
	OpsRepository // unused embedded repository methods
	// captured retains the supplied filter before err is inspected. A nonnil err
	// wins over resp; otherwise resp is returned when present and an absent
	// response produces an empty statistics result.
	resp     *OpsTokenStatsResponse // configured successful statistics response
	err      error                  // configured repository failure result
	captured *OpsTokenStatsFilter   // last supplied filter pointer
}

// GetTokenStats retains the supplied filter pointer before returning the
// configured error or response. A nil response produces an empty successful
// object, and subsequent caller mutation remains visible through captured.
func (s *tokenStatsRepoStub) GetTokenStats(ctx context.Context, filter *OpsTokenStatsFilter) (*OpsTokenStatsResponse, error) {
	s.captured = filter
	if s.err != nil {
		return nil, s.err
	}
	if s.resp != nil {
		return s.resp, nil
	}
	return &OpsTokenStatsResponse{}, nil
}

// TestOpsServiceGetTokenStats_Validation supplies absent or invalid times,
// group identity, and conflicting or excessive limits. Each subtest checks the
// service HTTP status and specific error reason for its listed input.
func TestOpsServiceGetTokenStats_Validation(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name       string               // validation case display name
		filter     *OpsTokenStatsFilter // submitted invalid filter fixture
		wantCode   int                  // expected HTTP status code
		wantReason string               // expected application error reason
	}{
		{
			name:       "filter 不能为空",
			filter:     nil,
			wantCode:   400,
			wantReason: "OPS_FILTER_REQUIRED",
		},
		{
			name: "start_time/end_time 必填",
			filter: &OpsTokenStatsFilter{
				StartTime: time.Time{},
				EndTime:   now,
			},
			wantCode:   400,
			wantReason: "OPS_TIME_RANGE_REQUIRED",
		},
		{
			name: "start_time 不能晚于 end_time",
			filter: &OpsTokenStatsFilter{
				StartTime: now,
				EndTime:   now.Add(-1 * time.Minute),
			},
			wantCode:   400,
			wantReason: "OPS_TIME_RANGE_INVALID",
		},
		{
			name: "group_id 必须大于 0",
			filter: &OpsTokenStatsFilter{
				StartTime: now.Add(-time.Hour),
				EndTime:   now,
				GroupID:   int64Ptr(0),
			},
			wantCode:   400,
			wantReason: "OPS_GROUP_ID_INVALID",
		},
		{
			name: "top_n 与分页参数互斥",
			filter: &OpsTokenStatsFilter{
				StartTime: now.Add(-time.Hour),
				EndTime:   now,
				TopN:      10,
				Page:      1,
			},
			wantCode:   400,
			wantReason: "OPS_PAGINATION_CONFLICT",
		},
		{
			name: "top_n 参数越界",
			filter: &OpsTokenStatsFilter{
				StartTime: now.Add(-time.Hour),
				EndTime:   now,
				TopN:      101,
			},
			wantCode:   400,
			wantReason: "OPS_TOPN_INVALID",
		},
		{
			name: "page_size 参数越界",
			filter: &OpsTokenStatsFilter{
				StartTime: now.Add(-time.Hour),
				EndTime:   now,
				Page:      1,
				PageSize:  101,
			},
			wantCode:   400,
			wantReason: "OPS_PAGE_SIZE_INVALID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &OpsService{
				opsRepo: &tokenStatsRepoStub{},
			}

			_, err := svc.GetTokenStats(context.Background(), tt.filter)
			require.Error(t, err)
			require.Equal(t, tt.wantCode, httpx.ErrorCode(err))
			require.Equal(t, tt.wantReason, apperror.Reason(err))
		})
	}
}

// TestOpsServiceGetTokenStats_DefaultPagination requests model statistics
// without pagination or TopN. It checks the filter passed to storage uses page
// one and twenty rows while TopN remains zero.
func TestOpsServiceGetTokenStats_DefaultPagination(t *testing.T) {
	now := time.Now().UTC()
	repo := &tokenStatsRepoStub{
		resp: &OpsTokenStatsResponse{
			Items: []*OpsTokenStatsItem{
				{Model: "gpt-4o-mini", RequestCount: 10},
			},
			Total: 1,
		},
	}
	svc := &OpsService{opsRepo: repo}

	filter := &OpsTokenStatsFilter{
		TimeRange: "30d",
		StartTime: now.Add(-30 * 24 * time.Hour),
		EndTime:   now,
	}
	resp, err := svc.GetTokenStats(context.Background(), filter)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, repo.captured)
	require.Equal(t, 1, repo.captured.Page)
	require.Equal(t, 20, repo.captured.PageSize)
	require.Equal(t, 0, repo.captured.TopN)
}

// TestOpsServiceGetTokenStats_RepoUnavailable requests a valid leading-model
// query from a service with no repository. It protects the unavailable HTTP
// status and repository-unavailable reason.
func TestOpsServiceGetTokenStats_RepoUnavailable(t *testing.T) {
	now := time.Now().UTC()
	svc := &OpsService{}

	_, err := svc.GetTokenStats(context.Background(), &OpsTokenStatsFilter{
		TimeRange: "1h",
		StartTime: now.Add(-time.Hour),
		EndTime:   now,
		TopN:      10,
	})
	require.Error(t, err)
	require.Equal(t, 503, httpx.ErrorCode(err))
	require.Equal(t, "OPS_REPO_UNAVAILABLE", apperror.Reason(err))
}

// int64Ptr returns a pointer to the supplied integer fixture value.
func int64Ptr(v int64) *int64 { return &v }
