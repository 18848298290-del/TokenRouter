package billing

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// redeemCreateRecorder captures codes submitted by the administration service.
type redeemCreateRecorder struct {
	RedeemCodeRepository
	created []RedeemCode
}

type redeemRepoStubForAdminList struct {
	RedeemCodeRepository

	listWithFiltersCalls  int
	listWithFiltersParams pagination.PaginationParams
	listWithFiltersType   string
	listWithFiltersStatus string
	listWithFiltersSearch string
	listWithFiltersCodes  []RedeemCode
	listWithFiltersResult *pagination.PaginationResult
	listWithFiltersErr    error
}

// TestRedeemAdmin_GenerateRedeemCodes_CustomCodeLength covers Han, mixed text, and supplementary-plane code points.
func TestRedeemAdmin_GenerateRedeemCodes_CustomCodeLength(t *testing.T) {
	tests := []struct {
		name    string
		code    string
		tooLong bool
	}{
		{name: "中文未满上限", code: strings.Repeat("兑", 11)},
		{name: "中文达到上限", code: strings.Repeat("兑", 32)},
		{name: "英文达到上限", code: strings.Repeat("a", 32)},
		{name: "中英混合达到上限", code: strings.Repeat("兑a", 16)},
		{name: "补充平面汉字达到上限", code: strings.Repeat("𠮷", 32)},
		{name: "首尾空白", code: " \t" + strings.Repeat("兑", 32) + "\n "},
		{name: "中文超过上限", code: strings.Repeat("兑", 33), tooLong: true},
		{name: "英文超过上限", code: strings.Repeat("a", 33), tooLong: true},
		{name: "补充平面汉字超过上限", code: strings.Repeat("𠮷", 33), tooLong: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &redeemCreateRecorder{}
			svc := NewRedeemAdmin(repo, nil, nil)

			codes, err := svc.GenerateRedeemCodes(context.Background(), &GenerateRedeemCodesInput{
				Code:  tt.code,
				Count: 1,
				Type:  RedeemTypeBalance,
				Value: 10,
			})

			if tt.tooLong {
				require.Error(t, err)
				require.Equal(t, "REDEEM_CODE_TOO_LONG", apperror.Reason(err))
				require.Empty(t, codes)
				require.Empty(t, repo.created)
				return
			}
			require.NoError(t, err)
			require.Len(t, codes, 1)
			require.Equal(t, strings.TrimSpace(tt.code), codes[0].Code)
			require.Equal(t, codes, repo.created)
		})
	}
}

// Create copies the code so tests can inspect the repository input.
func (r *redeemCreateRecorder) Create(_ context.Context, code *RedeemCode) error {
	r.created = append(r.created, *code)
	return nil
}

func TestAdminService_ListRedeemCodes_WithSearch(t *testing.T) {
	t.Run("search 参数正常传递到 repository 层", func(t *testing.T) {
		repo := &redeemRepoStubForAdminList{
			listWithFiltersCodes:  []RedeemCode{{ID: 4, Code: "ABC"}},
			listWithFiltersResult: &pagination.PaginationResult{Total: 3},
		}
		svc := NewRedeemAdmin(repo, nil, nil)

		codes, total, err := svc.ListRedeemCodes(context.Background(), 1, 20, RedeemTypeBalance, StatusUnused, "ABC", "value", "ASC")
		require.NoError(t, err)
		require.Equal(t, int64(3), total)
		require.Equal(t, []RedeemCode{{ID: 4, Code: "ABC"}}, codes)

		require.Equal(t, 1, repo.listWithFiltersCalls)
		require.Equal(t, pagination.PaginationParams{Page: 1, PageSize: 20, SortBy: "value", SortOrder: "ASC"}, repo.listWithFiltersParams)
		require.Equal(t, RedeemTypeBalance, repo.listWithFiltersType)
		require.Equal(t, StatusUnused, repo.listWithFiltersStatus)
		require.Equal(t, "ABC", repo.listWithFiltersSearch)
	})
}

func (s *redeemRepoStubForAdminList) ListWithFilters(_ context.Context, params pagination.PaginationParams, codeType, status, search string) ([]RedeemCode, *pagination.PaginationResult, error) {
	s.listWithFiltersCalls++
	s.listWithFiltersParams = params
	s.listWithFiltersType = codeType
	s.listWithFiltersStatus = status
	s.listWithFiltersSearch = search

	if s.listWithFiltersErr != nil {
		return nil, nil, s.listWithFiltersErr
	}

	result := s.listWithFiltersResult
	if result == nil {
		result = &pagination.PaginationResult{
			Total:    int64(len(s.listWithFiltersCodes)),
			Page:     params.Page,
			PageSize: params.PageSize,
		}
	}

	return s.listWithFiltersCodes, result, nil
}

func (s *redeemRepoStubForAdminList) ListByUserPaginated(_ context.Context, userID int64, params pagination.PaginationParams, codeType string) ([]RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected ListByUserPaginated call")
}

func (s *redeemRepoStubForAdminList) SumPositiveBalanceByUser(_ context.Context, userID int64) (float64, error) {
	panic("unexpected SumPositiveBalanceByUser call")
}
