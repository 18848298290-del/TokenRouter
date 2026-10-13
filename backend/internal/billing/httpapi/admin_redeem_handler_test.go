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

// batchUpdateRedeemRepoStub captures batch fields while rejecting unrelated
// credential persistence.
type batchUpdateRedeemRepoStub struct {
	// ids records each BatchUpdate call in fresh slice storage, preserving
	// requested order. Later changes to the caller's ID slice cannot alter this
	// captured sequence.
	ids []int64 // copied requested credential identifiers
	// fields is a shallow capture of BatchUpdate input. Optional pointers remain
	// shared with the request fields, and tests read them after synchronous
	// handler execution.
	fields billing.RedeemCodeBatchUpdateFields // last requested optional field values
}

// redeemAdminFixture exposes seeded response rows and captures administrator
// list/generation inputs.
type redeemAdminFixture struct {
	// redeems is returned by list and generation without copying the slice or
	// nested pointers. Tests can configure these records before requests and
	// observe shared response data.
	redeems []billing.RedeemCode // configured credential response records
	// lastListRedeemCodes stores the latest list criteria and cumulative
	// invocation count. Calls through both listing and export overwrite the
	// criteria on this unsynchronized fixture.
	lastListRedeemCodes struct {
		// codeType, status, search, sortBy and sortOrder capture one request's
		// filter/order inputs together. ListRedeemCodes overwrites these fields on
		// every call while calls counts all invocations.
		// codeType: requested credential category.
		// status: requested stored-status filter.
		// search: normalized search text.
		// sortBy: requested ordering column.
		// sortOrder: requested ordering direction.
		codeType, status, search, sortBy, sortOrder string
		calls                                       int // cumulative list invocation count
	}
	// lastGenerateRedeemCodes retains the supplied input pointer without copying
	// its nested values. Tests use nil to detect rejection before the
	// administrator generation method was called.
	lastGenerateRedeemCodes *billing.GenerateRedeemCodesInput // last generation request pointer
}

// TestCreateAndRedeem_TypeDefaultsToBalance submits a top-up without type to
// the validation fixture. It asserts the observed status is not bad request;
// the helper's recovered empty-service panic can satisfy that check.
func TestCreateAndRedeem_TypeDefaultsToBalance(t *testing.T) {
	h := newCreateAndRedeemHandler()
	code := postCreateAndRedeemValidation(t, h, map[string]any{
		"code":    "test-balance-default",
		"value":   10.0,
		"user_id": 1,
	})

	assert.NotEqual(t, http.StatusBadRequest, code,
		"omitting type should default to balance and pass validation")
}

// TestCreateAndRedeem_SubscriptionRequiresPlanID submits a subscription
// request with no plan pointer. The handler must return bad request before the
// empty redemption service is reached.
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

// TestCreateAndRedeem_SubscriptionRequiresPositivePlanID supplies zero and
// negative plan identifiers to the subscription endpoint. Both cases must
// return bad request before grant service access.
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

// TestCreateAndRedeem_SubscriptionValidParamsPassValidation supplies a
// positive plan identifier with required subscription fields. It checks the
// result differs from bad request, including the recovered empty-service panic
// result after validation.
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

// TestCreateAndRedeem_BalanceIgnoresSubscriptionFields supplies a balance
// request without a plan identifier. It checks for a result other than bad
// request; the fixture does not complete or assert a balance grant.
func TestCreateAndRedeem_BalanceIgnoresSubscriptionFields(t *testing.T) {
	h := newCreateAndRedeemHandler()
	code := postCreateAndRedeemValidation(t, h, map[string]any{
		"code":    "test-balance-no-extras",
		"type":    "balance",
		"value":   50.0,
		"user_id": 1,
	})

	assert.NotEqual(t, http.StatusBadRequest, code,
		"balance type should not require plan_id")
}

// TestResolveRedeemCodeExpiresAt_FromDays resolves a three-day relative expiry
// using the wall clock. It requires a nonnil result close to current UTC time
// plus three calendar days.
func TestResolveRedeemCodeExpiresAt_FromDays(t *testing.T) {
	days := 3

	expiresAt, err := ResolveRedeemCodeExpiresAt(nil, &days)

	require.NoError(t, err)
	require.NotNil(t, expiresAt)
	// Allow wall-clock advancement between resolution and the test's separate
	// observation.
	require.WithinDuration(t, time.Now().UTC().AddDate(0, 0, days), *expiresAt, 2*time.Second)
}

// TestResolveRedeemCodeExpiresAt_FromUnixSeconds supplies future whole-second
// epoch input to the expiry helper. It checks the returned epoch value and UTC
// location, protecting absolute-time encoding.
func TestResolveRedeemCodeExpiresAt_FromUnixSeconds(t *testing.T) {
	futureUnix := time.Now().UTC().Add(time.Hour).Unix()

	expiresAt, err := ResolveRedeemCodeExpiresAt(&futureUnix, nil)

	require.NoError(t, err)
	require.NotNil(t, expiresAt)
	require.Equal(t, futureUnix, expiresAt.Unix())
	require.Equal(t, time.UTC, expiresAt.Location())
}

// TestResolveRedeemCodeExpiresAt_ZeroMeansNoExpiry supplies an absolute zero
// with no relative input. It requires nil expiry and no error, preserving the
// clear/no-expiry representation.
func TestResolveRedeemCodeExpiresAt_ZeroMeansNoExpiry(t *testing.T) {
	zero := int64(0)

	expiresAt, err := ResolveRedeemCodeExpiresAt(&zero, nil)

	require.NoError(t, err)
	require.Nil(t, expiresAt)
}

// TestResolveRedeemCodeExpiresAt_RejectsPastAbsoluteTime supplies an epoch
// timestamp one minute before the wall-clock observation. It requires an error
// and nil expiry instead of accepting that elapsed timestamp.
func TestResolveRedeemCodeExpiresAt_RejectsPastAbsoluteTime(t *testing.T) {
	pastUnix := time.Now().UTC().Add(-time.Minute).Unix()

	expiresAt, err := ResolveRedeemCodeExpiresAt(&pastUnix, nil)

	require.Error(t, err)
	require.Nil(t, expiresAt)
}

// TestResolveRedeemCodeExpiresAt_RejectsNonPositiveDays supplies zero relative
// days to the expiry helper. It requires an error and nil result,
// distinguishing invalid relative input from absolute zero.
func TestResolveRedeemCodeExpiresAt_RejectsNonPositiveDays(t *testing.T) {
	days := 0

	expiresAt, err := ResolveRedeemCodeExpiresAt(nil, &days)

	require.Error(t, err)
	require.Nil(t, expiresAt)
}

// TestResolveRedeemCodeExpiresAt_RejectsConflictingInputs supplies both a
// future timestamp and three relative days. It requires an error and nil
// result so expiry selection cannot silently prefer one input.
func TestResolveRedeemCodeExpiresAt_RejectsConflictingInputs(t *testing.T) {
	futureUnix := time.Now().UTC().Add(time.Hour).Unix()
	days := 3

	expiresAt, err := ResolveRedeemCodeExpiresAt(&futureUnix, &days)

	require.Error(t, err)
	require.Nil(t, expiresAt)
}

// TestGenerate_CustomCodeLength submits thirty-two and thirty-three repeated
// Latin, BMP Chinese and supplementary Chinese characters. Accepted lengths
// reach the fixture unchanged; excessive lengths return bad request before
// generation is called.
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

				// The HTTP binding uses the credential field's thirty-two-code-point
				// ceiling, so supplementary characters consume one validation position
				// each.
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

// TestGenerate_AcceptsInvitationExpiry submits an invitation with a future
// absolute expiry to the generation handler. It requires successful HTTP
// output and the same epoch timestamp captured by the administrator fixture.
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
	// The literal POST method and route are valid request inputs. Construction
	// errors are discarded here before the resulting JSON request exercises
	// handler binding.
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/v1/admin/redeem-codes/generate", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.Generate(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, adminSvc.lastGenerateRedeemCodes)
	require.NotNil(t, adminSvc.lastGenerateRedeemCodes.ExpiresAt)
	require.Equal(t, futureUnix, adminSvc.lastGenerateRedeemCodes.ExpiresAt.Unix())
}

// TestRedeemBatchUpdate_NullExpiresAtClearsExpiry submits IDs, disabled
// status, notes and explicit null expiry through HTTP. It checks captured
// fields retain the clear's Set flag and nil time alongside the other
// requested changes.
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
	// The literal POST method and route are valid request inputs. Construction
	// errors are discarded here before the resulting JSON request exercises
	// handler binding.
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

// TestRedeemHandlerEndpoints exercises registered list, detail, generation,
// update, delete, batch-delete and expiry routes against the fixture. It
// checks expected HTTP statuses, including negative expiry rejection and the
// stats text failing numeric-ID parsing.
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

	// These endpoint smoke bodies contain JSON-supported strings and numeric
	// scalars. Their marshal errors are discarded, with HTTP status assertions
	// checking the subsequent handler responses.
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

// TestRedeemPaymentRequirementHTTP sends both false and true payment flags
// through generation and single update. It checks the captured generation
// input and both response encodings retain each supplied boolean value.
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
				RequiresPayment bool `json:"requires_payment"` // encoded paid-order eligibility flag
			} `json:"data"` // generated credential response records
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
				RequiresPayment bool `json:"requires_payment"` // encoded paid-order eligibility flag
			} `json:"data"` // updated credential response payload
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &updated))
		require.Equal(t, enabled, updated.Data.RequiresPayment)
	}
}

// TestRedeemSearchUnicode submits trimmed, blank, exact-limit and oversized
// Unicode/Latin searches to list and export. Both routes must issue one
// fixture query with the expected valid-UTF-8, one-hundred-code-point-bounded
// search text.
func TestRedeemSearchUnicode(t *testing.T) {
	code := "兑" + strings.Repeat("𠮷", 31)
	limit := strings.Repeat("兑𠮷", 50)
	tests := []struct {
		name   string // named search scenario case
		search string // supplied URL search text
		want   string // expected normalized query text
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

// TestRedeemExportPassesSearchAndSort supplies category, status, search and
// ascending value order to CSV export. It checks one administrator query
// receives every supplied filter and ordering value.
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

// TestRedeemExportSortDefaults exports with no sorting query parameters
// against the call-recording fixture. It checks one query uses the default ID
// column and descending order.
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

// newCreateAndRedeemHandler constructs a validation fixture around an empty
// redemption service. Successful parameter validation can reach unusable
// service dependencies and panic, which postCreateAndRedeemValidation converts
// to a zero status result.
func newCreateAndRedeemHandler() *AdminRedeemHandler {
	return NewAdminRedeemHandler(newRedeemAdminFixture(), &billing.RedeemService{})
}

// postCreateAndRedeemValidation binds a JSON request and returns its handler
// status or zero after any recovered panic. Marshal failure stops the test;
// the recovery allows callers to inspect validation responses with the
// deliberately empty service.
func postCreateAndRedeemValidation(t *testing.T, handler *AdminRedeemHandler, body any) (code int) {
	t.Helper()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	jsonBytes, err := json.Marshal(body)
	require.NoError(t, err)
	// The literal POST method and route are valid request inputs. Construction
	// errors are discarded here before the resulting JSON request exercises
	// handler binding.
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/v1/admin/redeem-codes/create-and-redeem", bytes.NewReader(jsonBytes))
	c.Request.Header.Set("Content-Type", "application/json")

	defer func() {
		if r := recover(); r != nil {
			// The deliberately empty service can panic after accepted parameters.
			// Convert recovery to a sentinel so these tests inspect validation without
			// completing a grant.
			code = 0
		}
	}()
	handler.CreateAndRedeem(c)
	return w.Code
}

// BatchUpdate copies the requested ID sequence and shallowly captures its
// optional fields. It returns the supplied ID count without persistence or
// validation, allowing the HTTP null-expiry test to inspect conversion output.
func (s *batchUpdateRedeemRepoStub) BatchUpdate(ctx context.Context, ids []int64, fields billing.RedeemCodeBatchUpdateFields) (int64, error) {
	s.ids = append([]int64(nil), ids...)
	s.fields = fields
	return int64(len(ids)), nil
}

// Create rejects single credential insertion with the unsupported-operation
// error. It leaves the supplied record unchanged, so this batch-capture
// fixture cannot satisfy service paths that require a persisted new code.
func (s *batchUpdateRedeemRepoStub) Create(ctx context.Context, code *billing.RedeemCode) error {
	return errors.New("not implemented")
}

// CreateBatch rejects every credential batch with the unsupported-operation
// error. It saves no supplied records and does not populate their IDs, leaving
// creation behavior outside this batch-update fixture.
func (s *batchUpdateRedeemRepoStub) CreateBatch(ctx context.Context, codes []billing.RedeemCode) error {
	return errors.New("not implemented")
}

// GetByID reports the billing not-found error for every record identifier. It
// returns no credential, so this fixture provides batch-field capture without
// stored records for administrator or recipient lookup.
func (s *batchUpdateRedeemRepoStub) GetByID(ctx context.Context, id int64) (*billing.RedeemCode, error) {
	return nil, billing.ErrRedeemCodeNotFound
}

// GetByIDForUpdate reports not found for every requested record ID. It
// acquires no row lock and supplies no credential, so tests requiring locked
// updates need a repository fixture that implements them.
func (s *batchUpdateRedeemRepoStub) GetByIDForUpdate(ctx context.Context, id int64) (*billing.RedeemCode, error) {
	return nil, billing.ErrRedeemCodeNotFound
}

// GetByCode reports the billing not-found error for every credential string.
// It supplies no stored recipient summary, so existing-code recovery cannot
// obtain a successful lookup from this capture fixture.
func (s *batchUpdateRedeemRepoStub) GetByCode(ctx context.Context, code string) (*billing.RedeemCode, error) {
	return nil, billing.ErrRedeemCodeNotFound
}

// GetByCodeForUpdate reports not found for every credential string before any
// grant can read a stored code. It acquires no lock and supplies no
// transaction-backed record for redemption tests.
func (s *batchUpdateRedeemRepoStub) GetByCodeForUpdate(ctx context.Context, code string) (*billing.RedeemCode, error) {
	return nil, billing.ErrRedeemCodeNotFound
}

// Update rejects credential replacement with the unsupported-operation error
// for any supplied record. It leaves input fields unchanged and persists no
// snapshot, so administrator edits cannot be exercised through this stub.
func (s *batchUpdateRedeemRepoStub) Update(ctx context.Context, code *billing.RedeemCode) error {
	return errors.New("not implemented")
}

// Delete rejects every requested credential deletion with the
// unsupported-operation error. It changes no stored state, so a caller cannot
// infer a successful removal from this batch-field capture fixture.
func (s *batchUpdateRedeemRepoStub) Delete(ctx context.Context, id int64) error {
	return errors.New("not implemented")
}

// Use rejects credential consumption with the unsupported-operation error for
// any record/recipient pair. It creates no usage and changes no count, leaving
// recipient redemption outside this batch-update fixture.
func (s *batchUpdateRedeemRepoStub) Use(ctx context.Context, id, userID int64) error {
	return errors.New("not implemented")
}

// CreateUsage rejects insertion of recipient history with the
// unsupported-operation error. It leaves the usage object unchanged and stores
// no record, so grant transaction tests require another persistence fixture.
func (s *batchUpdateRedeemRepoStub) CreateUsage(ctx context.Context, usage *billing.RedeemCodeUsage) error {
	return errors.New("not implemented")
}

// GetUsageByRedeemCodeAndUser returns the unsupported-operation error for any
// code/recipient lookup. It supplies no history observation, so the stub
// cannot establish whether a recipient has already redeemed a credential.
func (s *batchUpdateRedeemRepoStub) GetUsageByRedeemCodeAndUser(ctx context.Context, redeemCodeID, userID int64) (*billing.RedeemCodeUsage, error) {
	return nil, errors.New("not implemented")
}

// List rejects unfiltered page access with the unsupported-operation error. It
// returns neither credential rows nor pagination metadata, leaving list
// responses to redeemAdminFixture in these HTTP tests.
func (s *batchUpdateRedeemRepoStub) List(ctx context.Context, params pagination.PaginationParams) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	return nil, nil, errors.New("not implemented")
}

// ListWithFilters rejects filtered credential queries with the
// unsupported-operation error. It returns no page or count metadata, so search
// and sorting tests use the separate administrator fixture.
func (s *batchUpdateRedeemRepoStub) ListWithFilters(ctx context.Context, params pagination.PaginationParams, codeType, status, search string) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	return nil, nil, errors.New("not implemented")
}

// ListByUserPaginated rejects recipient history listing with the
// unsupported-operation error. It returns no credential receipts or page
// metadata, keeping this capture stub focused on the batch-update conversion
// test.
func (s *batchUpdateRedeemRepoStub) ListByUserPaginated(ctx context.Context, userID int64, params pagination.PaginationParams, codeType string) ([]billing.RedeemCode, *pagination.PaginationResult, error) {
	return nil, nil, errors.New("not implemented")
}

// SumPositiveBalanceByUser returns zero with the unsupported-operation error
// for any recipient. The error means no balance-history total was read, so
// callers cannot treat that companion zero as a successful aggregate.
func (s *batchUpdateRedeemRepoStub) SumPositiveBalanceByUser(ctx context.Context, userID int64) (float64, error) {
	return 0, errors.New("not implemented")
}

// setupRedeemExportRouter registers CSV export with a fresh call-recording
// administrator fixture. It leaves authentication and command replay unbound,
// and the returned fixture exposes the service parameters used by that export
// request.
func setupRedeemExportRouter() (*gin.Engine, *redeemAdminFixture) {
	router := gin.New()
	adminSvc := newRedeemAdminFixture()

	h := NewAdminRedeemHandler(adminSvc, nil)
	router.GET("/api/v1/admin/redeem-codes/export", h.Export)
	return router, adminSvc
}

// newRedeemAdminFixture creates a fixture with one unused balance credential
// and a current UTC creation time. Each invocation owns fresh outer slice
// storage, which listing and generation subsequently share with callers.
func newRedeemAdminFixture() *redeemAdminFixture {
	return &redeemAdminFixture{redeems: []billing.RedeemCode{{ID: 5, Code: "R-TEST", Type: billing.RedeemTypeBalance, Value: 10, Status: billing.StatusUnused, CreatedAt: time.Now().UTC()}}}
}

// setupRedeemAdminContractRouter registers the administrator credential routes
// with a fresh seeded fixture. The handler's redemption service is nil, so
// grant/batch-update routes require separate fixtures while administration and
// export remain available.
func setupRedeemAdminContractRouter() (*gin.Engine, *redeemAdminFixture) {
	router := gin.New()
	source := newRedeemAdminFixture()
	handler := NewAdminRedeemHandler(source, nil)
	RegisterRedeemCodeRoutes(router.Group("/api/v1/admin"), handler)
	return router, source
}

// ListRedeemCodes records the latest filters/order and increments its
// unsynchronized call counter. It ignores paging/filter selection and returns
// the shared configured rows with their length as total, which lets HTTP tests
// inspect forwarding.
func (s *redeemAdminFixture) ListRedeemCodes(ctx context.Context, page, pageSize int, codeType, status, search string, sortBy, sortOrder string) ([]billing.RedeemCode, int64, error) {
	s.lastListRedeemCodes.codeType = codeType
	s.lastListRedeemCodes.status = status
	s.lastListRedeemCodes.search = search
	s.lastListRedeemCodes.sortBy = sortBy
	s.lastListRedeemCodes.sortOrder = sortOrder
	s.lastListRedeemCodes.calls++
	return s.redeems, int64(len(s.redeems)), nil
}

// GetRedeemCode returns a fresh unused fixture credential carrying the
// requested ID and fixed text. It accepts every identifier and reads no shared
// row, so detail tests observe handler mapping only.
func (s *redeemAdminFixture) GetRedeemCode(ctx context.Context, id int64) (*billing.RedeemCode, error) {
	code := billing.RedeemCode{ID: id, Code: "R-TEST", Status: billing.StatusUnused}
	return &code, nil
}

// GenerateRedeemCodes retains the supplied request pointer and returns the
// configured shared rows. It performs no creation or entitlement validation,
// allowing tests to distinguish handler binding from administrator policy.
func (s *redeemAdminFixture) GenerateRedeemCodes(ctx context.Context, input *billing.GenerateRedeemCodesInput) ([]billing.RedeemCode, error) {
	s.lastGenerateRedeemCodes = input
	return s.redeems, nil
}

// UpdateRedeemCode constructs a fresh unused fixture result and applies
// supplied optional fields. Expiry uses ExpiresAtSet for clearing, and a
// supplied plan marks the result subscription; the shared seeded rows remain
// unchanged.
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

// DeleteRedeemCode reports successful fixture deletion for every record ID
// without changing seeded rows. HTTP endpoint tests use this result to check
// response status rather than persisted removal or eligibility rules.
func (s *redeemAdminFixture) DeleteRedeemCode(ctx context.Context, id int64) error {
	return nil
}

// BatchDeleteRedeemCodes returns the supplied sequence length with no error or
// persistence. Repeated IDs are counted repeatedly, and seeded rows remain
// available, so endpoint tests observe count forwarding only.
func (s *redeemAdminFixture) BatchDeleteRedeemCodes(ctx context.Context, ids []int64) (int64, error) {
	return int64(len(ids)), nil
}

// ExpireRedeemCode constructs a fixture marked used for the requested record
// ID. It reports success and leaves the shared seeded rows unchanged, so
// endpoint tests observe HTTP response mapping rather than expiry persistence.
func (s *redeemAdminFixture) ExpireRedeemCode(ctx context.Context, id int64) (*billing.RedeemCode, error) {
	code := billing.RedeemCode{ID: id, Code: "R-TEST", Status: billing.StatusUsed}
	return &code, nil
}
