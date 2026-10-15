package postgres

import (
	"context"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/paymentorder"
	"github.com/TokenFlux/TokenRouter/ent/paymentproviderinstance"
	"github.com/TokenFlux/TokenRouter/internal/payment"
)

// instances.go reads payment provider instances and aggregates their recorded
// daily payment amounts for selection.
// Provider selection observes unfinished order money by instance and creation
// time, while paid-usage reads follow confirmation time and a narrower status
// set. Both aggregates use stored PayAmount values, so pending creation and
// later payment can contribute to different daily observations.
//
// Instance reads retain scalar configuration and limits for the payment core
// to interpret. Enabled selection sorts by configured order and an optional
// exact adapter key; historical single-instance lookup can still read disabled
// rows. Batch usage converts any decoded rows alongside its scan error,
// allowing the selector to retain partial observations after logging failure.
// The bound Ent client remains caller-owned, and this adapter performs no
// additional currency conversion or transaction-context client selection.

// InstanceStore reads and writes payment instances through its caller-owned
// Ent client.
type InstanceStore struct {
	// client is the bound persistence connection; callers keep it usable and
	// own its transaction or database lifecycle.
	client *dbent.Client
}

// NewInstanceStore retains the supplied Ent client for payment instance
// persistence. The caller owns that client's lifetime; methods use this bound
// client and do not select another client from transaction context.
func NewInstanceStore(client *dbent.Client) *InstanceStore { return &InstanceStore{client: client} }

// InstanceFromEntity returns a newly allocated value containing the entity's
// scalar instance fields. Nil input returns nil, while configuration and limit
// strings remain exactly as stored for later payment interpretation.
func InstanceFromEntity(v *dbent.PaymentProviderInstance) *payment.ProviderInstance {
	if v == nil {
		return nil
	}
	return &payment.ProviderInstance{
		ID:              v.ID,
		ProviderKey:     v.ProviderKey,
		Name:            v.Name,
		Config:          v.Config,
		SupportedTypes:  v.SupportedTypes,
		Enabled:         v.Enabled,
		PaymentMode:     v.PaymentMode,
		SortOrder:       v.SortOrder,
		Limits:          v.Limits,
		RefundEnabled:   v.RefundEnabled,
		AllowUserRefund: v.AllowUserRefund,
		CreatedAt:       v.CreatedAt,
		UpdatedAt:       v.UpdatedAt,
	}
}

// EnabledInstances returns enabled instances sorted by configured sort_order,
// optionally narrowed by an exact nonempty provider key. Equal sort values
// receive no additional tie key here; conversion preserves the stored scalar
// values.
func (s *InstanceStore) EnabledInstances(ctx context.Context, key string) ([]*payment.ProviderInstance, error) {
	query := s.client.PaymentProviderInstance.Query().Where(paymentproviderinstance.Enabled(true))
	if key != "" {
		query = query.Where(paymentproviderinstance.ProviderKey(key))
	}
	rows, err := query.Order(dbent.Asc(paymentproviderinstance.FieldSortOrder)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*payment.ProviderInstance, len(rows))
	for i, v := range rows {
		out[i] = InstanceFromEntity(v)
	}
	return out, nil
}

// Instance reads one local payment instance ID and converts its stored value.
func (s *InstanceStore) Instance(ctx context.Context, id int64) (*payment.ProviderInstance, error) {
	v, err := s.client.PaymentProviderInstance.Get(ctx, id)
	return InstanceFromEntity(v), err
}

// DailyUsage sums PayAmount by supplied instance IDs for pending, processing,
// paid, completed, and recharging orders created at or after start. It returns
// a new result map and the scan error together.
//
// The time filter follows creation rather than payment confirmation, so
// unfinished orders contribute to selection observations. Missing instance
// groups have no map entry. Rows supplied before a scan error are still
// converted and returned; the selector logs that error while reading any
// returned partial map. Values are raw stored amounts, with no currency
// conversion performed by this aggregate.
func (s *InstanceStore) DailyUsage(ctx context.Context, ids []string, start time.Time) (map[string]float64, error) {
	var rows []struct {
		// grouped decimal instance ID
		InstanceID string  `json:"provider_instance_id"`
		Sum        float64 `json:"sum"` // summed stored gateway amounts
	}
	err := s.client.PaymentOrder.Query().
		Where(
			paymentorder.ProviderInstanceIDIn(ids...),
			paymentorder.StatusIn(payment.OrderStatusPending, payment.OrderStatusProcessing, payment.OrderStatusPaid, payment.OrderStatusCompleted, payment.OrderStatusRecharging),
			paymentorder.CreatedAtGTE(start),
		).
		GroupBy(paymentorder.FieldProviderInstanceID).
		Aggregate(dbent.Sum(paymentorder.FieldPayAmount)).
		Scan(ctx, &rows)
	// Return decoded rows even when the scan reports failure. The selector
	// retains any supplied group totals and treats only missing IDs as zero after
	// logging that error.
	out := make(map[string]float64, len(rows))
	for _, r := range rows {
		out[r.InstanceID] = r.Sum
	}
	return out, err
}

// PaidDailyAmount sums stored PayAmount for one instance across completed,
// paid, and recharging orders confirmed at or after start. Scan errors return,
// while an empty successful row set yields zero for callers interpreting paid
// usage.
func (s *InstanceStore) PaidDailyAmount(ctx context.Context, id string, start time.Time) (float64, error) {
	var rows []struct {
		Sum float64 `json:"sum"` // summed confirmed gateway amounts
	}
	err := s.client.PaymentOrder.Query().
		Where(
			paymentorder.ProviderInstanceID(id),
			paymentorder.StatusIn(payment.OrderStatusCompleted, payment.OrderStatusPaid, payment.OrderStatusRecharging),
			paymentorder.PaidAtGTE(start),
		).
		Aggregate(dbent.Sum(paymentorder.FieldPayAmount)).
		Scan(ctx, &rows)
	if err != nil {
		return 0, err
	}
	if len(rows) > 0 {
		return rows[0].Sum, nil
	}
	// An empty successful aggregate supplies no paid amount observation. Return
	// zero so usage callers can continue with that explicit empty-result
	// interpretation.
	return 0, nil
}

// CountEnabledInstances counts enabled rows for the exact adapter key used by
// legacy webhook resolution. It includes no method filter; ProviderBindings
// uses this observation to reject ambiguous registry choice.
func (s *InstanceStore) CountEnabledInstances(ctx context.Context, key string) (int, error) {
	return s.client.PaymentProviderInstance.Query().Where(paymentproviderinstance.ProviderKeyEQ(key), paymentproviderinstance.EnabledEQ(true)).Count(ctx)
}

// OrderByTradeNumber reads the exact stored merchant number and converts its
// order observation. Webhook candidate selection uses this lookup before
// verification; no legacy numeric alias expansion occurs here.
func (s *InstanceStore) OrderByTradeNumber(ctx context.Context, no string) (*payment.Order, error) {
	o, e := s.client.PaymentOrder.Query().Where(paymentorder.OutTradeNo(no)).Only(ctx)
	return OrderFromEntity(o), e
}
