package usageprovider

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/upstream/usagecontract"
	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// TestParseDerouterUsage normalizes numeric and decimal-string account amounts.
// Available funds, including zero and debt, become the wallet remainder while
// locked funds never appear as historical spending or lifetime allowance.
func TestParseDerouterUsage(t *testing.T) {
	for _, test := range []struct {
		name string  // account amount scenario
		body string  // upstream response JSON
		want float64 // expected spendable USD
	}{
		{"strings", `{"balance":"100.0000","locked_balance":"20.0000","available":"80.0000"}`, 80},
		{"numbers", `{"balance":100,"locked_balance":20,"available":80}`, 80},
		{"mixed", `{"balance":"0.3","locked_balance":0.1,"available":"0.2"}`, 0.2},
		{"zero", `{"balance":"20","locked_balance":"20","available":"0"}`, 0},
		{"debt", `{"balance":"-1.5","locked_balance":"0","available":"-1.5"}`, -1.5},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseDerouterUsage([]byte(test.body))
			require.NoError(t, err)
			require.NoError(t, usageview.ValidateNormalizedUsage(got))
			require.Equal(t, usageview.UpstreamUsageAdapterDerouter, got.Provider)
			require.Equal(t, "balance", got.Mode)
			require.Equal(t, "USD", got.Unit)
			require.Equal(t, test.want, *got.Balance.Remaining)
			require.Nil(t, got.Balance.Used)
			require.Nil(t, got.Balance.Total)
			require.Nil(t, got.Subscription)
		})
	}
}

// TestParseDerouterUsageRejectsInvalidAmounts supplies missing, malformed, and
// inconsistent account observations. Each response must fail instead of
// publishing a zero or nonfinite balance that could trigger a reminder.
func TestParseDerouterUsageRejectsInvalidAmounts(t *testing.T) {
	for _, body := range []string{
		`{}`, `null`, `[]`, `{"error":"invalid key"}`, `not json`,
		`{"balance":"100","locked_balance":"20"}`,
		`{"balance":null,"locked_balance":0,"available":0}`,
		`{"balance":100,"locked_balance":null,"available":100}`,
		`{"balance":100,"locked_balance":0,"available":null}`,
		`{"balance":100,"locked_balance":20,"available":100}`,
		`{"balance":100,"locked_balance":-20,"available":120}`,
		`{"balance":100,"locked_balance":20,"available":true}`,
		`{"balance":"NaN","locked_balance":0,"available":0}`,
		`{"balance":100,"locked_balance":0,"available":"Infinity"}`,
		`{"balance":"1e999","locked_balance":0,"available":"1e999"}`,
		`{"balance":100,"locked_balance":0,"available":""}`,
		`{"balance":100,"locked_balance":0,"available":" 100 "}`,
		`{"balance":-1e308,"locked_balance":1e308,"available":-1e308}`,
	} {
		t.Run(body, func(t *testing.T) {
			got, err := ParseDerouterUsage([]byte(body))
			require.ErrorIs(t, err, usageview.ErrUpstreamUsageInvalidResponse)
			require.Nil(t, got)
		})
	}
}

// TestDerouterUsageQueryRejectsHTTPFailures supplies valid-looking balances
// behind failed HTTP statuses. Authentication, throttling, missing endpoints,
// redirects, and timeouts must remain errors rather than publish wallet data.
func TestDerouterUsageQueryRejectsHTTPFailures(t *testing.T) {
	for _, test := range []struct {
		name         string // HTTP failure scenario
		status       int    // upstream HTTP status
		transportErr error  // simulated transport failure
		want         error  // expected public error
	}{
		{"unauthorized", http.StatusUnauthorized, nil, usageview.ErrUpstreamUsageAuthFailed},
		{"forbidden", http.StatusForbidden, nil, usageview.ErrUpstreamUsageAuthFailed},
		{"rate limited", http.StatusTooManyRequests, nil, usageview.ErrUpstreamUsageRateLimited},
		{"missing endpoint", http.StatusNotFound, nil, usageview.ErrUpstreamUsageUnsupported},
		{"redirect", http.StatusFound, nil, usageview.ErrUpstreamUsageInvalidResponse},
		{"server failure", http.StatusInternalServerError, nil, usageview.ErrUpstreamUsageInvalidResponse},
		{"timeout", 0, context.DeadlineExceeded, usageview.ErrUpstreamUsageTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			input := &usagecontract.Request{
				BaseURL: "https://management.example.test", APIKey: "fixture-account-key",
				Context:      func(ctx context.Context) context.Context { return ctx },
				ApplyHeaders: func(http.Header) {},
				Do: func(*http.Request) (*http.Response, error) {
					calls++
					if test.transportErr != nil {
						return nil, test.transportErr
					}
					return &http.Response{
						StatusCode: test.status,
						Body:       io.NopCloser(strings.NewReader(`{"balance":100,"locked_balance":20,"available":80}`)),
					}, nil
				},
			}
			result, err := (&DerouterUsageAdapter{}).Query(context.Background(), input)
			require.ErrorIs(t, err, test.want)
			require.Nil(t, result)
			require.Equal(t, 1, calls)
		})
	}
}
