package billing

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// redeemCreateRecorder captures shallow credential copies submitted by
// administrator generation.
type redeemCreateRecorder struct {
	// RedeemCodeRepository supplies unused fixture methods through a nil embedded
	// interface. These tests call only the overridden methods; another embedded
	// call would panic.
	RedeemCodeRepository // unused repository fixture methods
	// created owns the appended record slice, but its optional field pointers
	// remain those supplied by the caller. Tests inspect captured values before
	// changing those inputs.
	created []RedeemCode // captured shallow credential records
}

// redeemRepoStubForAdminList captures administrator listing inputs and
// supplies configured page results.
type redeemRepoStubForAdminList struct {
	// RedeemCodeRepository supplies unused fixture methods through a nil embedded
	// interface. These tests call only the overridden methods; another embedded
	// call would panic.
	RedeemCodeRepository // unused repository fixture methods

	// listWithFiltersCalls, listWithFiltersParams, listWithFiltersType,
	// listWithFiltersStatus and listWithFiltersSearch retain the current request
	// before its configured result is returned. The synchronous test inspects one
	// captured call.
	listWithFiltersCalls  int                         // captured administrator list call count
	listWithFiltersParams pagination.PaginationParams // captured pagination and sort fields
	listWithFiltersType   string                      // captured credential type filter
	listWithFiltersStatus string                      // captured credential status filter
	listWithFiltersSearch string                      // captured code or recipient search
	// listWithFiltersCodes and listWithFiltersResult supply successful returns
	// when listWithFiltersErr is nil. Their slice/pointer storage remains shared,
	// and a missing result is synthesized from the rows.
	listWithFiltersCodes  []RedeemCode                 // configured returned credential rows
	listWithFiltersResult *pagination.PaginationResult // optional configured page metadata
	// listWithFiltersErr takes precedence over configured rows and page metadata.
	// A non-nil error makes the method return nil companion results after
	// capturing the request.
	listWithFiltersErr error // injected administrator listing error
}

// TestRedeemAdmin_GenerateRedeemCodes_CustomCodeLength submits Han, Latin,
// mixed and supplementary-plane text below, at and above thirty-two code points. It
// checks trimming, stored valid inputs and rejected overlong inputs before
// creation.
func TestRedeemAdmin_GenerateRedeemCodes_CustomCodeLength(t *testing.T) {
	tests := []struct {
		name    string // test case display name
		code    string // supplied Unicode credential text
		tooLong bool   // expected code-point length rejection
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

// Create appends a shallow copy of the supplied non-nil credential and returns
// success. Optional pointers remain shared with the caller, so this fixture
// checks captured values rather than independent nested ownership.
func (r *redeemCreateRecorder) Create(_ context.Context, code *RedeemCode) error {
	r.created = append(r.created, *code)
	return nil
}

// TestAdminService_ListRedeemCodes_WithSearch supplies type/status/search and
// sort parameters for an administrator page. It checks one captured request,
// forwarded pagination fields and the configured rows/total returned by the
// fixture.
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

// ListWithFilters captures the listing request before returning configured
// error or data. Missing result metadata is synthesized from the fixture slice
// length and supplied page parameters; returned rows and configured page
// pointer remain shared with the stub.
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
		// No page pointer was configured, so synthesize the fixture total from
		// captured rows while keeping this request's supplied page and size.
		result = &pagination.PaginationResult{
			Total:    int64(len(s.listWithFiltersCodes)),
			Page:     params.Page,
			PageSize: params.PageSize,
		}
	}

	return s.listWithFiltersCodes, result, nil
}

// ListByUserPaginated panics if the administrator-list fixture is used for
// individual history. These cases expect ListWithFilters, so this method
// identifies an unexpected user-history read before returning data.
func (s *redeemRepoStubForAdminList) ListByUserPaginated(_ context.Context, userID int64, params pagination.PaginationParams, codeType string) ([]RedeemCode, *pagination.PaginationResult, error) {
	panic("unexpected ListByUserPaginated call")
}

// SumPositiveBalanceByUser panics if administrator-list cases attempt
// balance-history aggregation. The fixture supplies listing results only, so
// this call identifies an unexpected total query outside their exercised path.
func (s *redeemRepoStubForAdminList) SumPositiveBalanceByUser(_ context.Context, userID int64) (float64, error) {
	panic("unexpected SumPositiveBalanceByUser call")
}
