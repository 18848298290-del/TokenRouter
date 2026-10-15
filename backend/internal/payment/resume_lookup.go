package payment

import (
	"context"
	"fmt"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// resume_lookup.go matches signed continuation claims against stored order
// identity before recovering payment state.
// A signed resume token identifies a known local order and can constrain its
// saved buyer, instance, provider, and visible method. Historical snapshot
// pins take precedence during those comparisons, keeping recovery tied to
// recorded identity while optional absent claims remain compatible.
//
// Pending and expired orders can query provider state through the ordinary
// confirmation path, then reread accepted paid or processing transitions.
// Other states return the saved order observation, and CheckPaid can suppress
// an unavailable provider query into unchanged state. This entry point
// establishes token/order matching; the signer owns signature and expiry
// parsing, while the HTTP response converter chooses the public fields exposed
// to the browser.

// GetPublicOrderByResumeToken parses a trimmed signed token and reads its
// local order identity. A positive user claim and nonempty provider/method
// claims constrain the returned order, while absent optional claims leave
// those comparisons unrequested.
//
// Nonempty snapshot identity takes precedence over order columns. Instance
// text compares exactly, provider key compares case-insensitively, and method
// comparison uses visible normalization. Pending or expired orders can query
// the provider through CheckPaid; accepted paid/processing outcomes trigger a
// reread, while suppressed query errors can leave the saved state unchanged.
// CanonicalReturnURL is not compared by this lookup, and other order states
// return their persisted observation.
func (s *OrderLifecycle) GetPublicOrderByResumeToken(ctx context.Context, token string) (*Order, error) {
	claims, err := s.resume.ParseToken(strings.TrimSpace(token))
	if err != nil {
		return nil, err
	}

	order, err := s.store.Order(ctx, claims.OrderID)
	if err != nil {
		if s.store.IsNotFound(err) {
			return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
		}
		return nil, fmt.Errorf("get order by resume token: %w", err)
	}
	if claims.UserID > 0 && order.UserID != claims.UserID {
		return nil, InvalidResumeTokenMatchError()
	}
	snapshot := PsOrderProviderSnapshot(order)
	orderProviderInstanceID := strings.TrimSpace(refundStringValue(order.ProviderInstanceID))
	orderProviderKey := strings.TrimSpace(refundStringValue(order.ProviderKey))
	// Recorded snapshot identity can outlive changes to compatibility columns.
	// Compare token claims with each nonempty snapshot pin before considering the
	// retained column value.
	if snapshot != nil {
		if snapshot.ProviderInstanceID != "" {
			orderProviderInstanceID = snapshot.ProviderInstanceID
		}
		if snapshot.ProviderKey != "" {
			orderProviderKey = snapshot.ProviderKey
		}
	}
	if claims.ProviderInstanceID != "" && orderProviderInstanceID != claims.ProviderInstanceID {
		return nil, InvalidResumeTokenMatchError()
	}
	if claims.ProviderKey != "" && !strings.EqualFold(orderProviderKey, claims.ProviderKey) {
		return nil, InvalidResumeTokenMatchError()
	}
	if claims.PaymentType != "" && NormalizeVisibleMethod(order.PaymentType) != NormalizeVisibleMethod(claims.PaymentType) {
		return nil, InvalidResumeTokenMatchError()
	}
	// These saved states can still recover a missed payment observation. Query
	// through the shared confirmation path, then reread only after an accepted
	// paid or processing outcome.
	if order.Status == OrderStatusPending || order.Status == OrderStatusExpired {
		result, checkErr := s.CheckPaid(ctx, order)
		if checkErr != nil {
			return nil, checkErr
		}
		if result == LifecycleCheckPaidResultAlreadyPaid || result == LifecycleCheckPaidResultProcessing {
			order, err = s.store.Order(ctx, order.ID)
			if err != nil {
				return nil, fmt.Errorf("reload order by resume token: %w", err)
			}
		}
	}

	return order, nil
}

// InvalidResumeTokenMatchError returns the bad-request error for mismatched
// token/order identity.
func InvalidResumeTokenMatchError() error {
	return infraerrors.BadRequest("INVALID_RESUME_TOKEN", "resume token does not match the payment order")
}

// ParseWeChatPaymentResumeToken trims caller text and delegates to the shared
// resume signer's typed WeChat parser. Missing signing configuration,
// malformed signatures, token-type mismatch, or expired claims return that
// parser's error.
func (s *OrderLifecycle) ParseWeChatPaymentResumeToken(token string) (*WeChatPaymentResumeClaims, error) {
	return s.resume.ParseWeChatPaymentResumeToken(strings.TrimSpace(token))
}
