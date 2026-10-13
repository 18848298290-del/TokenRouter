//go:build integration

package promotion_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/promocodeusage"
	billingpostgres "github.com/TokenFlux/TokenRouter/internal/billing/postgres"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/identity/httpapi/dto"
	identitypostgres "github.com/TokenFlux/TokenRouter/internal/identity/postgres"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/promotion"
	promotionhttp "github.com/TokenFlux/TokenRouter/internal/promotion/httpapi"
	promotionpostgres "github.com/TokenFlux/TokenRouter/internal/promotion/postgres"
)

// Promo redemption credits balance and cumulative recharge together with one
// usage row and the code's usage count. PostgreSQL trigger fixtures reject
// either usage insertion or count advancement after observing the credit inside
// that transaction, while callback counters and outside reads inspect rollback
// or successful publication.
//
// The successful case retries the same user and code to check duplicate
// rejection and retained credited balance. Its usage response is compared with
// the shallow public identity DTO and checked for the supplied password hash.
// Cleanup removes injected triggers before deleting dependent usage rows and
// their code, and the plain callback counters observe synchronous fixture calls
// rather than providing concurrent state protection.

// promotionInvalidationProbe counts observed authentication and wallet
// invalidation calls from promo scenarios.
type promotionInvalidationProbe struct {
	// auth and balance count their corresponding callback invocations
	// regardless of user ID. Calls and test reads are serialized because
	// these ordinary integers have no lock.
	auth, balance int // auth callback count; balance callback count
}

// TestPromotionApplyCodeFundsAndUsageAtomicity injects usage or count failures
// after transaction-local promo credit, alongside a successful case. Funds,
// usage, counts, and invalidations follow the commit result, and usage JSON
// excludes the supplied password hash.
func TestPromotionApplyCodeFundsAndUsageAtomicity(t *testing.T) {
	for _, failure := range []string{"", "usage", "count"} {
		t.Run("failure_"+failure, func(t *testing.T) {
			ctx := context.Background()
			client, integrationDB := testStore(t)
			user, err := client.User.Create().SetEmail("test-promo@example.com").SetPasswordHash("must-not-export").SetUsername("promo").SetBalance(5).Save(ctx)
			require.NoError(t, err)
			repo := promotionpostgres.NewPromoCodeRepository(client)
			code := &promotion.PromoCode{Code: "REFUNDPROMO", BonusAmount: 10, MaxUses: 2, Status: promotion.PromoCodeStatusActive}
			require.NoError(t, repo.Create(ctx, code))
			// Remove usage references before deleting their code so
			// normal case cleanup satisfies foreign keys; later
			// trigger cleanup runs first when a failure fixture
			// exists.
			t.Cleanup(func() {
				_, e := client.PromoCodeUsage.Delete().Where(promocodeusage.PromoCodeIDEQ(code.ID)).Exec(ctx)
				require.NoError(t, e)
				require.NoError(t, repo.Delete(ctx, code.ID))
			})
			mutations := promotionpostgres.NewPromoMutations(client, func(tx *dbent.Tx) promotionpostgres.PromoBalance { return billingpostgres.BalanceInTx(tx) })
			invalidation := &promotionInvalidationProbe{}
			svc := promotion.NewPromoService(repo, mutations, invalidation, invalidation, promotion.Runtime{Now: time.Now})
			if failure != "" {
				table, event, condition := "promo_code_usages", "INSERT", fmt.Sprintf("NEW.promo_code_id=%d", code.ID)
				if failure == "count" {
					table, event, condition = "promo_codes", "UPDATE", fmt.Sprintf("NEW.id=%d AND NEW.used_count > OLD.used_count", code.ID)
				}
				name := fmt.Sprintf("test_promo_failure_%d", code.ID)
				// Observe credited balance in the failure
				// trigger before rejecting the selected usage
				// or count write, exposing a credit participant
				// outside the common transaction.
				_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF %s THEN IF (SELECT balance FROM users WHERE id=%d)<>15 THEN RAISE EXCEPTION 'credit not in same transaction'; END IF; RAISE EXCEPTION 'test promo forced failure'; END IF; RETURN NEW; END; $$`, name, condition, user.ID))
				require.NoError(t, err)
				_, err = integrationDB.ExecContext(ctx, fmt.Sprintf("CREATE TRIGGER %s BEFORE %s ON %s FOR EACH ROW EXECUTE FUNCTION %s()", name, event, table, name))
				require.NoError(t, err)
				// Remove the selected failure trigger before
				// its function so subsequent usage and code
				// cleanup can run after the transaction
				// rollback has completed.
				t.Cleanup(func() {
					_, e := integrationDB.ExecContext(ctx, fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON %s; DROP FUNCTION IF EXISTS %s()", name, table, name))
					require.NoError(t, e)
				})
			}
			err = svc.ApplyPromoCode(ctx, user.ID, code.Code)
			current, readErr := client.User.Get(ctx, user.ID)
			require.NoError(t, readErr)
			saved, readErr := repo.GetByID(ctx, code.ID)
			require.NoError(t, readErr)
			count, readErr := client.PromoCodeUsage.Query().Where(promocodeusage.PromoCodeIDEQ(code.ID)).Count(ctx)
			require.NoError(t, readErr)
			if failure != "" {
				require.ErrorContains(t, err, "test promo forced failure")
				require.Equal(t, 5.0, current.Balance)
				require.Zero(t, current.TotalRecharged)
				require.Zero(t, count)
				require.Zero(t, saved.UsedCount)
				require.Zero(t, invalidation.auth)
				require.Zero(t, invalidation.balance)
				return
			}
			require.NoError(t, err)
			require.Equal(t, 15.0, current.Balance)
			require.Equal(t, 10.0, current.TotalRecharged)
			require.Equal(t, 1, count)
			require.Equal(t, 1, saved.UsedCount)
			require.Equal(t, 1, invalidation.auth)
			require.Equal(t, 1, invalidation.balance)
			require.ErrorIs(t, svc.ApplyPromoCode(ctx, user.ID, code.Code), promotion.ErrPromoCodeAlreadyUsed)
			current, readErr = client.User.Get(ctx, user.ID)
			require.NoError(t, readErr)
			require.Equal(t, 15.0, current.Balance)
			// Usage responses match the shallow public user DTO and
			// exclude the password hash.
			usages, _, err := repo.ListUsagesByPromoCode(ctx, code.ID, pagination.PaginationParams{Page: 1, PageSize: 20})
			require.NoError(t, err)
			require.Len(t, usages, 1)
			actual, err := json.Marshal(promotionhttp.PromoCodeUsageFromService(&usages[0]).User)
			require.NoError(t, err)
			expected, err := json.Marshal(dto.UserFromIdentityShallow[json.RawMessage](identity.CopyUser(identitypostgres.UserFromEntity(current))))
			require.NoError(t, err)
			require.JSONEq(t, string(expected), string(actual))
			require.NotContains(t, string(actual), "must-not-export")
		})
	}
}

// InvalidateAuthCacheByUserID increments the observed authentication callback
// count and ignores the supplied context and user ID. Callers serialize access
// because this fixture uses an ordinary integer without synchronization.
func (p *promotionInvalidationProbe) InvalidateAuthCacheByUserID(context.Context, int64) { p.auth++ }

// InvalidateUserBalance increments the observed balance callback count and
// always returns nil for any context or user ID. Callers serialize its ordinary
// integer mutation while inspecting publication order in this fixture.
func (p *promotionInvalidationProbe) InvalidateUserBalance(context.Context, int64) error {
	p.balance++
	return nil
}
