package postgres

import (
	"context"
	"strconv"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/paymentauditlog"
	"github.com/TokenFlux/TokenRouter/ent/paymentorder"
	"github.com/TokenFlux/TokenRouter/ent/paymentproviderinstance"
	"github.com/TokenFlux/TokenRouter/internal/payment"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

// management.go persists provider instance changes and counts order references
// used by management checks.
// Instance management uses optional field replacements and protects selected
// configuration observations with an expected stored string. Provider and plan
// order counts remain separate reads used by ConfigService before those
// persistence operations, so they do not supply a shared lock over later
// creation or deletion.
//
// Queries can request enabled rows, configured ordering, bounds, exclusion, or
// refund-eligible IDs independently. Refund eligibility selects IDs after
// checking both switches and leaves the other scalar fields zero.
// Force-expired protection first reads matching order IDs, then counts their
// text-keyed audit actions; a status change can occur between those
// observations. The storage update owns its config compare and conflict
// translation, while business validation and subsequent provider refresh
// belong to its callers.

// IsNotFound reports whether Ent classified the error as a missing entity.
func (s *InstanceStore) IsNotFound(err error) bool { return dbent.IsNotFound(err) }

// ListInstances applies the independent supplied instance filters and returns
// converted scalar values. Empty provider key, nonpositive exclude ID, and
// nonpositive limit add no corresponding predicate or row bound; sorting is
// requested separately. UserRefundEligible additionally requires both refund
// flags and selects only IDs, leaving other converted fields at zero.
func (s *InstanceStore) ListInstances(ctx context.Context, f payment.InstanceFilter) ([]*payment.ProviderInstance, error) {
	q := s.client.PaymentProviderInstance.Query()
	if f.EnabledOnly {
		q = q.Where(paymentproviderinstance.EnabledEQ(true))
	}
	if f.ProviderKey != "" {
		q = q.Where(paymentproviderinstance.ProviderKeyEQ(f.ProviderKey))
	}
	if f.ExcludeID > 0 {
		q = q.Where(paymentproviderinstance.IDNEQ(f.ExcludeID))
	}
	if f.SortByOrder {
		q = q.Order(paymentproviderinstance.BySortOrder())
	}
	if f.Limit > 0 {
		q = q.Limit(f.Limit)
	}
	var rows []*dbent.PaymentProviderInstance
	var err error
	if f.UserRefundEligible {
		rows, err = q.Where(
			paymentproviderinstance.RefundEnabledEQ(true),
			paymentproviderinstance.AllowUserRefundEQ(true),
		).Select(paymentproviderinstance.FieldID).All(ctx)
	} else {
		rows, err = q.All(ctx)
	}
	if err != nil {
		return nil, err
	}
	out := make([]*payment.ProviderInstance, len(rows))
	for i, row := range rows {
		out[i] = InstanceFromEntity(row)
	}
	return out, nil
}

// CreateInstance persists the supplied scalar instance values through Ent and
// converts the result. ConfigService validates management input first; this
// storage method stages those values directly and does not construct an
// adapter.
func (s *InstanceStore) CreateInstance(ctx context.Context, v payment.ProviderInstance) (*payment.ProviderInstance, error) {
	value, err := s.client.PaymentProviderInstance.Create().
		SetProviderKey(v.ProviderKey).
		SetName(v.Name).
		SetConfig(v.Config).
		SetSupportedTypes(v.SupportedTypes).
		SetEnabled(v.Enabled).
		SetPaymentMode(v.PaymentMode).
		SetSortOrder(v.SortOrder).
		SetLimits(v.Limits).
		SetRefundEnabled(v.RefundEnabled).
		SetAllowUserRefund(v.AllowUserRefund).
		Save(ctx)
	return InstanceFromEntity(value), err
}

// UpdateInstance applies nonnil patch fields to the addressed instance and
// returns its converted value. Nil fields retain their columns, and
// ExpectedConfig adds equality against the current stored string.
//
// That compare condition protects a caller's configuration observation from a
// competing config change. Ent not-found with an expectation becomes
// locale.ErrConflict, covering changed or absent rows; other errors propagate.
// The method does not repeat ConfigService's pending-order or refund-flag
// planning, so those earlier observations remain separate from this update.
func (s *InstanceStore) UpdateInstance(ctx context.Context, id int64, v payment.InstancePatch) (*payment.ProviderInstance, error) {
	q := s.client.PaymentProviderInstance.UpdateOneID(id)
	if v.ExpectedConfig != nil {
		q.Where(paymentproviderinstance.ConfigEQ(*v.ExpectedConfig))
	}
	if v.Name != nil {
		q.SetName(*v.Name)
	}
	if v.Config != nil {
		q.SetConfig(*v.Config)
	}
	if v.SupportedTypes != nil {
		q.SetSupportedTypes(*v.SupportedTypes)
	}
	if v.Enabled != nil {
		q.SetEnabled(*v.Enabled)
	}
	if v.SortOrder != nil {
		q.SetSortOrder(*v.SortOrder)
	}
	if v.Limits != nil {
		q.SetLimits(*v.Limits)
	}
	if v.RefundEnabled != nil {
		q.SetRefundEnabled(*v.RefundEnabled)
	}
	if v.AllowUserRefund != nil {
		q.SetAllowUserRefund(*v.AllowUserRefund)
	}
	if v.PaymentMode != nil {
		q.SetPaymentMode(*v.PaymentMode)
	}
	row, err := q.Save(ctx)
	// An expected config turns a changed or vanished row into a caller conflict.
	// Preserve that distinction from an ordinary missing-instance update whose
	// expectation was absent.
	if dbent.IsNotFound(err) && v.ExpectedConfig != nil {
		return nil, locale.ErrConflict
	}
	return InstanceFromEntity(row), err
}

// DeleteInstance physically removes one addressed payment instance through
// Ent. ConfigService checks protected order references before calling it; this
// persistence operation performs no additional in-progress or force-expired
// reference query.
func (s *InstanceStore) DeleteInstance(ctx context.Context, id int64) error {
	return s.client.PaymentProviderInstance.DeleteOneID(id).Exec(ctx)
}

// CountInProgressByProvider counts orders whose stored decimal instance ID and
// current status match the management protection set. It is a separate
// observation used before configuration changes, disablement, or deletion.
func (s *InstanceStore) CountInProgressByProvider(ctx context.Context, id int64) (int, error) {
	// Order instance bindings use decimal text while management supplies a local
	// int64 ID. Format that identity before counting the protected states for
	// this exact instance.
	return s.client.PaymentOrder.Query().Where(
		paymentorder.ProviderInstanceIDEQ(strconv.FormatInt(id, 10)),
		paymentorder.StatusIn(payment.InProgressOrderStatuses()...),
	).Count(ctx)
}

// CountInProgressByPlan counts orders linked to the plan in pending,
// processing, paid, or recharging states. Billing plan deletion uses that
// separate observation to protect unfinished payment references.
func (s *InstanceStore) CountInProgressByPlan(ctx context.Context, id int64) (int, error) {
	return s.client.PaymentOrder.Query().Where(
		paymentorder.PlanIDEQ(id),
		paymentorder.StatusIn(payment.InProgressOrderStatuses()...),
	).Count(ctx)
}

// CountForcedExpiredByProvider counts force-expiration audit rows for IDs
// first observed as expired and bound to the instance. Empty ID results return
// zero before reading audit history.
//
// Local integer IDs become decimal strings because audit order identity uses
// text. The second query counts ORDER_FORCE_EXPIRED for that selected set; it
// does not recheck order status or lock those rows. Concurrent transitions can
// therefore change eligibility between the two observations, and ConfigService
// applies the returned count before its separate deletion step.
func (s *InstanceStore) CountForcedExpiredByProvider(ctx context.Context, id int64) (int, error) {
	ids, err := s.client.PaymentOrder.Query().Where(
		paymentorder.ProviderInstanceIDEQ(strconv.FormatInt(id, 10)),
		paymentorder.StatusEQ(payment.OrderStatusExpired),
	).IDs(ctx)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	// Audit order IDs use decimal text while order queries return int64 IDs.
	// Convert the observed expired set before querying its force-expiration
	// actions.
	auditIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		auditIDs = append(auditIDs, strconv.FormatInt(id, 10))
	}
	return s.client.PaymentAuditLog.Query().Where(
		paymentauditlog.OrderIDIn(auditIDs...),
		paymentauditlog.ActionEQ("ORDER_FORCE_EXPIRED"),
	).Count(ctx)
}
