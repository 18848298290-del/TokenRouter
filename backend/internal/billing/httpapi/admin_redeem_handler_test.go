package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

type batchUpdateRedeemRepoStub struct {
	ids    []int64
	fields billing.RedeemCodeBatchUpdateFields
}

// redeemAdminFixture supplies redemption records and captures request parameters.
type redeemAdminFixture struct {
	redeems             []billing.RedeemCode
	lastListRedeemCodes struct {
		codeType, status, search, sortBy, sortOrder string
		calls                                       int
	}
	lastGenerateRedeemCodes *billing.GenerateRedeemCodesInput
}

func TestCreateAndRedeem_TypeDefaultsToBalance(t *testing.T) {
	// Missing type uses balance validation. The empty service panics after validation; the helper returns zero.
	h := newCreateAndRedeemHandler()
	code := postCreateAndRedeemValidation(t, h, map[string]any{
		"code":    "test-balance-default",
		"value":   10.0,
		"user_id": 1,
	})

	assert.NotEqual(t, http.StatusBadRequest, code,
		"omitting type should default to balance and pass validation")
}

func TestCreateAndRedeem_SubscriptionRequiresPlanID(t *testing.T) {
	h := newCreateAndRedeemHandler()
	code := postCreateAndRedeemValidation(t, h, map[string]any{
		"code":    "test-sub-no-plan",
		"type":    "subscription",
		"value":   29.9,
		"user_id": 1,
	})

	assert.Equal(t, http.StatusBadRequest, code)
}

func TestCreateAndRedeem_SubscriptionRequiresPositivePlanID(t *testing.T) {
	h := newCreateAndRedeemHandler()

	t.Run("zero", func(t *testing.T) {
		code := postCreateAndRedeemValidation(t, h, map[string]any{
			"code":    "test-sub-bad-plan-zero",
			"type":    "subscription",
			"value":   29.9,
			"user_id": 1,
			"plan_id": 0,
		})

		assert.Equal(t, http.StatusBadRequest, code)
	})

	t.Run("negative", func(t *testing.T) {
		code := postCreateAndRedeemValidation(t, h, map[string]any{
			"code":    "test-sub-bad-plan-negative",
			"type":    "subscription",
			"value":   29.9,
			"user_id": 1,
			"plan_id": -7,
		})

		assert.Equal(t, http.StatusBadRequest, code)
	})
}

func TestCreateAndRedeem_SubscriptionValidParamsPassValidation(t *testing.T) {
	planID := int64(5)
	h := newCreateAndRedeemHandler()
	code := postCreateAndRedeemValidation(t, h, map[string]any{
		"code":    "test-sub-valid",
		"type":    "subscription",
		"value":   29.9,
		"user_id": 1,
		"plan_id": planID,
	})

	assert.NotEqual(t, http.StatusBadRequest, code,
		"valid subscription params should pass validation")
}

func TestCreateAndRedeem_BalanceIgnoresSubscriptionFields(t *testing.T) {
	h := newCreateAndRedeemHandler()
	// Balance requests may omit plan_id.
	code := postCreateAndRedeemValidation(t, h, map[string]any{
		"code":    "test-balance-no-extras",
		"type":    "balance",
		"value":   50.0,
		"user_id": 1,
	})

	assert.NotEqual(t, http.StatusBadRequest, code,
		"balance type should not require plan_id")
}

func TestResolveRedeemCodeExpiresAt_FromDays(t *testing.T) {
	days := 3

	expiresAt, err := ResolveRedeemCodeExpiresAt(nil, &days)

	require.NoError(t, err)
	require.NotNil(t, expiresAt)
	require.WithinDuration(t, time.Now().UTC().AddDate(0, 0, days), *expiresAt, 2*time.Second)
}

func TestResolveRedeemCodeExpiresAt_FromUnixSeconds(t *testing.T) {
	futureUnix := time.Now().UTC().Add(time.Hour).Unix()

	expiresAt, err := ResolveRedeemCodeExpiresAt(&futureUnix, nil)

	require.NoError(t, err)
	require.NotNil(t, expiresAt)
	require.Equal(t, futureUnix, expiresAt.Unix())
	require.Equal(t, time.UTC, expiresAt.Location())
}

func TestResolveRedeemCodeExpiresAt_ZeroMeansNoExpiry(t *testing.T) {
	zero := int64(0)

	expiresAt, err := ResolveRedeemCodeExpiresAt(&zero, nil)

	require.NoError(t, err)
	require.Nil(t, expiresAt)
}

func TestResolveRedeemCodeExpiresAt_RejectsPastAbsoluteTime(t *testing.T) {
	pastUnix := time.Now().UTC().Add(-time.Minute).Unix()

	expiresAt, err := ResolveRedeemCodeExpiresAt(&pastUnix, nil)

	require.Error(t, err)
	require.Nil(t, expiresAt)
}

func TestResolveRedeemCodeExpiresAt_RejectsNonPositiveDays(t *testing.T) {
	days := 0

	expiresAt, err := ResolveRedeemCodeExpiresAt(nil, &days)

	require.Error(t, err)
	require.Nil(t, expiresAt)
}

func TestResolveRedeemCodeExpiresAt_RejectsConflictingInputs(t *testing.T) {
	futureUnix := time.Now().UTC().Add(time.Hour).Unix()
	days := 3

	expiresAt, err := ResolveRedeemCodeExpiresAt(&futureUnix, &days)

	require.Error(t, err)
	require.Nil(t, expiresAt)
}

// TestGenerate_CustomCodeLength checks the 32-code-point limit, including supplementary Unicode characters.
func TestGenerate_CustomCodeLength(t *testing.T) {
	for _, char := range []string{"兑", "a", "𠮷"} {
		for _, length := range []int{32, 33} {
			code := strings.Repeat(char, length)
			t.Run(code, func(t *testing.T) {
				router, fixture := setupRedeemAdminContractRouter()
				body, err := json.Marshal(map[string]any{
					"code":  code,
					"count": 1,
					"type":  "balance",
					"value": 10,
				})
				require.NoError(t, err)
				req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/redeem-codes/generate", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()

				router.ServeHTTP(w, req)

				if length > 32 {
					require.Equal(t, http.StatusBadRequest, w.Code)
					require.Nil(t, fixture.lastGenerateRedeemCodes)
					return
				}
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				require.NotNil(t, fixture.lastGenerateRedeemCodes)
				require.Equal(t, code, fixture.lastGenerateRedeemCodes.Code)
			})
		}
	}
}

func TestGenerate_AcceptsInvitationExpiry(t *testing.T) {
	adminSvc := newRedeemAdminFixture()
	h := NewAdminRedeemHandler(adminSvc, nil)
	futureUnix := time.Now().UTC().Add(time.Hour).Unix()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body, err := json.Marshal(map[string]any{
		"count":      1,
		"type":       "invitation",
		"value":      0,
		"expires_at": futureUnix,
	})
	require.NoError(t, err)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/v1/admin/redeem-codes/generate", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.Generate(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, adminSvc.lastGenerateRedeemCodes)
	require.NotNil(t, adminSvc.lastGenerateRedeemCodes.ExpiresAt)
	require.Equal(t, futureUnix, adminSvc.lastGenerateRedeemCodes.ExpiresAt.Unix())
}

func TestRedeemBatchUpdate_NullExpiresAtClearsExpiry(t *testing.T) {
	status := billing.StatusDisabled
	notes := "批量维护"
	repo := &batchUpdateRedeemRepoStub{}
	redeemSvc := billing.NewRedeemService(repo, nil, nil, nil, nil, nil, nil, nil, billing.RedeemRuntime{})
	h := NewAdminRedeemHandler(newRedeemAdminFixture(), redeemSvc)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body, err := json.Marshal(map[string]any{
		"ids": []int64{1, 2},
		"fields": map[string]any{
			"status":     status,
			"expires_at": nil,
			"notes":      notes,
		},
	})
	require.NoError(t, err)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/v1/admin/redeem-codes/batch-update", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.BatchUpdate(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, []int64{1, 2}, repo.ids)
	require.Equal(t, &status, repo.fields.Status)
	require.True(t, repo.fields.ExpiresAt.Set)
	require.Nil(t, repo.fields.ExpiresAt.Value)
	require.Equal(t, &notes, repo.fields.Notes)
}

func TestRedeemHandlerEndpoints(t *testing.T) {
	router, _ := setupRedeemAdminContractRouter()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/redeem-codes", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/redeem-codes/5", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	body, _ := json.Marshal(map[string]any{"count": 1, "type": "balance", "value": 10})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/redeem-codes/generate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	body, _ = json.Marshal(map[string]any{"max_uses": 2, "expires_at": 0})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/admin/redeem-codes/5", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	body, _ = json.Marshal(map[string]any{"expires_at": -1})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/admin/redeem-codes/5", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/admin/redeem-codes/5", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/redeem-codes/batch-delete", bytes.NewBufferString(`{"ids":[1,2]}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/redeem-codes/5/expire", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/redeem-codes/stats", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestRedeemPaymentRequirementHTTP checks payment requirements in generation, updates, and responses.
func TestRedeemPaymentRequirementHTTP(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		router, fixture := setupRedeemAdminContractRouter()
		fixture.redeems[0].RequiresPayment = enabled
		body, err := json.Marshal(map[string]any{"count": 1, "type": "balance", "value": 10, "requires_payment": enabled})
		require.NoError(t, err)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/redeem-codes/generate", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, enabled, fixture.lastGenerateRedeemCodes.RequiresPayment)
		var generated struct {
			Data []struct {
				RequiresPayment bool `json:"requires_payment"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &generated))
		require.Len(t, generated.Data, 1)
		require.Equal(t, enabled, generated.Data[0].RequiresPayment)

		body, err = json.Marshal(map[string]any{"requires_payment": enabled})
		require.NoError(t, err)
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodPut, "/api/v1/admin/redeem-codes/5", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		var updated struct {
			Data struct {
				RequiresPayment bool `json:"requires_payment"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &updated))
		require.Equal(t, enabled, updated.Data.RequiresPayment)
	}
}

// TestRedeemSearchUnicode checks list/export parity and code-point truncation of long search terms.
func TestRedeemSearchUnicode(t *testing.T) {
	code := "兑" + strings.Repeat("𠮷", 31)
	limit := strings.Repeat("兑𠮷", 50)
	tests := []struct {
		name   string
		search string
		want   string
	}{
		{name: "完整兑换码", search: code, want: code},
		{name: "首尾空白", search: " \t" + code + "\n ", want: code},
		{name: "恰好一百字符", search: limit, want: limit},
		{name: "超过一百字符", search: limit + "尾", want: limit},
		{name: "英文超过上限", search: strings.Repeat("a", 101), want: strings.Repeat("a", 100)},
		{name: "空白搜索", search: " \t\n ", want: ""},
	}
	for _, route := range []string{"/api/v1/admin/redeem-codes", "/api/v1/admin/redeem-codes/export"} {
		for _, tt := range tests {
			t.Run(route+"/"+tt.name, func(t *testing.T) {
				router, fixture := setupRedeemAdminContractRouter()
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, route+"?search="+url.QueryEscape(tt.search), nil)

				router.ServeHTTP(rec, req)

				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Equal(t, 1, fixture.lastListRedeemCodes.calls)
				require.True(t, utf8.ValidString(fixture.lastListRedeemCodes.search))
				require.Equal(t, tt.want, fixture.lastListRedeemCodes.search)
			})
		}
	}
}

func TestRedeemExportPassesSearchAndSort(t *testing.T) {
	router, adminSvc := setupRedeemExportRouter()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/redeem-codes/export?type=balance&status=unused&search=ABC&sort_by=value&sort_order=asc", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, 1, adminSvc.lastListRedeemCodes.calls)
	require.Equal(t, "balance", adminSvc.lastListRedeemCodes.codeType)
	require.Equal(t, "unused", adminSvc.lastListRedeemCodes.status)
	require.Equal(t, "ABC", adminSvc.lastListRedeemCodes.search)
	require.Equal(t, "value", adminSvc.lastListRedeemCodes.sortBy)
	require.Equal(t, "asc", adminSvc.lastListRedeemCodes.sortOrder)
}

func TestRedeemExportSortDefaults(t *testing.T) {
	router, adminSvc := setupRedeemExportRouter()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/redeem-codes/export", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, 1, adminSvc.lastListRedeemCodes.calls)
	require.Equal(t, "id", adminSvc.lastListRedeemCodes.sortBy)
	require.Equal(t, "desc", adminSvc.lastListRedeemCodes.sortOrder)
}

// newCreateAndRedeemHandler uses an empty RedeemService for request validation tests.
func newCreateAndRedeemHandler() *AdminRedeemHandler {
	return NewAdminRedeemHandler(newRedeemAdminFixture(), &billing.RedeemService{})
}

// postCreateAndRedeemValidation calls CreateAndRedeem and returns its HTTP status.
// It returns zero when the empty service panics after parameter validation.
func postCreateAndRedeemValidation(t *testing.T, handler *AdminRedeemHandler, body any) (code int) {
	t.Helper()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	jsonBytes, err := json.Marshal(body)
	require.NoError(t, err)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/v1/admin/redeem-codes/create-and-redeem", bytes.NewReader(jsonBytes))
	c.Request.Header.Set("Content-Type", "application/json")

	defer func() {
		if r := recover(); r != nil {
			// Zero marks a recovered panic from the empty service.
			code = 0
		}
	}()
	handler.CreateAndRedeem(c)
	return w.Code
}

func (s *batchUpdateRedeemRepoStub) BatchUpdate(ctx context.Context, ids []int64, fields billing.RedeemCodeBatchUpdateFields) (int64, error) {
	s.ids = append([]int64(nil), ids...)
	s.fields = fields
	return int64(len(ids)), nil
}

func (s *batchUpdateRedeemRepoStub) Create(ctx context.Context, code *billing.RedeemCode) error {
	return errors.New("not implemented")
}

func (s *batchUpdateRedeemRepoStub) CreateBatch(ctx context.Context, codes []billing.RedeemCode) error {
	return errors.New("not implemented")
}

func (s *batchUpdateRedeemRepoStub) GetByID(ctx context.Context, id int64) (*billing.RedeemCode, error) {
	return nil, billing.ErrRedeemCodeNotFound
}

func (s *batchUpdateRedeemRepoStub) GetByIDForUpdate(ctx context.Context, id int64) (*billing.RedeemCode, error) {
	return nil, billing.ErrRedeemCodeNotFound
}

func (s *batchUpdateRedeemRepoStub) GetByCode(ctx context.Context, code string) (*billing.RedeemCode, error) {
	return nil, billing.ErrRedeemCodeNotFound
}

func (s *batchUpdateRedeemRepoStub) GetByCodeForUpdate(ctx context.Context, code string) (*billing.RedeemCode, error) {
	return nil, billing.ErrRedeemCodeNotFound
}

func (s *batchUpdateRedeemRepoStub) Update(ctx context.Context, code *billing.RedeemCode) error {
	return errors.New("not implemented")
}

func (s *batchUpdateRedeemRepoStub) Delete(ctx context.Context, id int64) error {
	return errors.New("not implemented")
}

func (s *batchUpdateRedeemRepoStub) Use(ctx context.Context, id, userID int64) error {
	return errors.New("not implemented")
}

func (s *batchUpdateRedeemRepoStub) CreateUsage(ctx context.Context, usage *billing.RedeemCodeUsage) error {
	return errors.New("not implemented")
}

func (s *batchUpdateRedeemRepoStub) GetUsageByRedeemCodeAndUser(ctx context.Context, redeemCodeID, userID int64) (*billing.RedeemCodeUsage, error) {
	return nil, errors.New("not implemented")
}

func (s *batchUpdateRedeemRepoStub) List(ctx context.Context, params pagination.PaginationParams) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	return nil, nil, errors.New("not implemented")
}

func (s *batchUpdateRedeemRepoStub) ListWithFilters(ctx context.Context, params pagination.PaginationParams, codeType, status, search string) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	return nil, nil, errors.New("not implemented")
}

func (s *batchUpdateRedeemRepoStub) ListByUserPaginated(ctx context.Context, userID int64, params pagination.PaginationParams, codeType string) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	return nil, nil, errors.New("not implemented")
}

func (s *batchUpdateRedeemRepoStub) SumPositiveBalanceByUser(ctx context.Context, userID int64) (float64, error) {
	return 0, errors.New("not implemented")
}

// setupRedeemExportRouter registers the export route and returns its call-recording fixture.
func setupRedeemExportRouter() (*gin.Engine, *redeemAdminFixture) {
	router := gin.New()
	adminSvc := newRedeemAdminFixture()

	h := NewAdminRedeemHandler(adminSvc, nil)
	router.GET("/api/v1/admin/redeem-codes/export", h.Export)
	return router, adminSvc
}

func newRedeemAdminFixture() *redeemAdminFixture {
	return &redeemAdminFixture{redeems: []billing.RedeemCode{{ID: 5, Code: "R-TEST", Type: billing.RedeemTypeBalance, Value: 10, Status: billing.StatusUnused, CreatedAt: time.Now().UTC()}}}
}

// setupRedeemAdminContractRouter assembles redemption management handlers and routes for tests.
func setupRedeemAdminContractRouter() (*gin.Engine, *redeemAdminFixture) {
	router := gin.New()
	source := newRedeemAdminFixture()
	handler := NewAdminRedeemHandler(source, nil)
	RegisterRedeemCodeRoutes(router.Group("/api/v1/admin"), handler)
	return router, source
}

func (s *redeemAdminFixture) ListRedeemCodes(ctx context.Context, page, pageSize int, codeType, status, search string, sortBy, sortOrder string) ([]billing.RedeemCode, int64, error) {
	s.lastListRedeemCodes.codeType = codeType
	s.lastListRedeemCodes.status = status
	s.lastListRedeemCodes.search = search
	s.lastListRedeemCodes.sortBy = sortBy
	s.lastListRedeemCodes.sortOrder = sortOrder
	s.lastListRedeemCodes.calls++
	return s.redeems, int64(len(s.redeems)), nil
}

func (s *redeemAdminFixture) GetRedeemCode(ctx context.Context, id int64) (*billing.RedeemCode, error) {
	code := billing.RedeemCode{ID: id, Code: "R-TEST", Status: billing.StatusUnused}
	return &code, nil
}

func (s *redeemAdminFixture) GenerateRedeemCodes(ctx context.Context, input *billing.GenerateRedeemCodesInput) ([]billing.RedeemCode, error) {
	s.lastGenerateRedeemCodes = input
	return s.redeems, nil
}

func (s *redeemAdminFixture) UpdateRedeemCode(ctx context.Context, id int64, input *billing.UpdateRedeemCodeInput) (*billing.RedeemCode, error) {
	code := billing.RedeemCode{ID: id, Code: "R-TEST", Status: billing.StatusUnused, MaxUses: 1}
	if input.RequiresPayment != nil {
		code.RequiresPayment = *input.RequiresPayment
	}
	if input.MaxUses != nil {
		code.MaxUses = *input.MaxUses
	}
	if input.Value != nil {
		code.Value = *input.Value
	}
	if input.ExpiresAtSet {
		code.ExpiresAt = input.ExpiresAt
	}
	if input.PlanID != nil {
		code.Type = billing.RedeemTypeSubscription
		code.PlanID = input.PlanID
	}
	return &code, nil
}

func (s *redeemAdminFixture) DeleteRedeemCode(ctx context.Context, id int64) error {
	return nil
}

func (s *redeemAdminFixture) BatchDeleteRedeemCodes(ctx context.Context, ids []int64) (int64, error) {
	return int64(len(ids)), nil
}

func (s *redeemAdminFixture) ExpireRedeemCode(ctx context.Context, id int64) (*billing.RedeemCode, error) {
	code := billing.RedeemCode{ID: id, Code: "R-TEST", Status: billing.StatusUsed}
	return &code, nil
}
