package postgres

import (
	"context"
	"database/sql"
	"errors"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	dbgroup "github.com/TokenFlux/TokenRouter/ent/group"
	dbprovider "github.com/TokenFlux/TokenRouter/ent/provider"
	dbprovidergroup "github.com/TokenFlux/TokenRouter/ent/providergroup"
	dbproxy "github.com/TokenFlux/TokenRouter/ent/proxy"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/accessview"
)

func (r *ProviderStore) Create(ctx context.Context, record *provider.Record) error {
	if err := CreateRecord(ctx, r.client, record); err != nil {
		return err
	}
	if err := r.publish(ctx, r.sql, record.ID, record.GroupIDs); err != nil {
		r.observe("[SchedulerOutbox] enqueue provider create failed: provider=%d err=%v", record.ID, err)
	}
	return nil
}

func CreateRecord(ctx context.Context, client *dbent.Client, record *provider.Record) error {
	if record == nil {
		return provider.ErrProviderNilInput
	}
	provider.DiscardDeprecatedExtra(record.Extra)

	builder := client.Provider.Create().
		SetName(record.Name).
		SetNillableNotes(record.Notes).
		SetPlatform(record.Platform).
		SetType(record.Type).
		SetCredentials(normalizeJSONMap(record.Credentials)).
		SetExtra(normalizeJSONMap(record.Extra)).
		SetConcurrency(record.Concurrency).
		SetPriority(record.Priority).
		SetStatus(record.Status).
		SetErrorMessage(record.ErrorMessage).
		SetSchedulable(record.Schedulable).
		SetAutoPauseOnExpired(record.AutoPauseOnExpired)

	if record.RateMultiplier != nil {
		builder.SetRateMultiplier(*record.RateMultiplier)
	}
	if record.LoadFactor != nil {
		builder.SetLoadFactor(*record.LoadFactor)
	}

	if record.ProxyID != nil {
		builder.SetProxyID(*record.ProxyID)
	}
	if record.LastUsedAt != nil {
		builder.SetLastUsedAt(*record.LastUsedAt)
	}
	if record.ExpiresAt != nil {
		builder.SetExpiresAt(*record.ExpiresAt)
	}
	if record.RateLimitedAt != nil {
		builder.SetRateLimitedAt(*record.RateLimitedAt)
	}
	if record.RateLimitResetAt != nil {
		builder.SetRateLimitResetAt(*record.RateLimitResetAt)
	}
	if record.OverloadUntil != nil {
		builder.SetOverloadUntil(*record.OverloadUntil)
	}
	if record.SessionWindowStart != nil {
		builder.SetSessionWindowStart(*record.SessionWindowStart)
	}
	if record.SessionWindowEnd != nil {
		builder.SetSessionWindowEnd(*record.SessionWindowEnd)
	}
	if record.SessionWindowStatus != "" {
		builder.SetSessionWindowStatus(record.SessionWindowStatus)
	}

	builder.SetQuotaDimension(dbprovider.QuotaDimension(record.QuotaDimensionOrDefault()))
	if record.ParentProviderID != nil {
		builder.SetParentProviderID(*record.ParentProviderID)
	}

	created, err := builder.Save(ctx)
	if err != nil {
		return translatePersistenceError(err, provider.ErrProviderNotFound, nil)
	}

	record.ID = created.ID
	record.CreatedAt = created.CreatedAt
	record.UpdatedAt = created.UpdatedAt
	return nil
}

// CreateWithProviderGroups persists the provider, group memberships, and scheduler outbox event
// in one transaction so routing snapshots can include the new provider and its groups together.
func (r *ProviderStore) CreateWithProviderGroups(ctx context.Context, record *provider.Record, groups []provider.GroupMembership) error {
	if record == nil {
		return provider.ErrProviderNilInput
	}
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return err
	}

	var txClient *dbent.Client
	if err == nil {
		defer func() { _ = tx.Rollback() }()
		txClient = tx.Client()
	} else {
		// Reuse the caller's transaction when the store's client is already transactional.
		txClient = r.client
	}

	if err := CreateRecord(ctx, txClient, record); err != nil {
		return err
	}
	groupIDs := make([]int64, 0, len(groups))
	if len(groups) > 0 {
		builders := make([]*dbent.ProviderGroupCreate, 0, len(groups))
		for i := range groups {
			groups[i].ProviderID = record.ID
			groupIDs = append(groupIDs, groups[i].GroupID)
			builders = append(builders, txClient.ProviderGroup.Create().
				SetProviderID(record.ID).
				SetGroupID(groups[i].GroupID),
			)
		}
		if _, err := txClient.ProviderGroup.CreateBulk(builders...).Save(ctx); err != nil {
			return err
		}
	}
	record.GroupIDs = groupIDs
	record.ProviderGroups = append([]provider.GroupMembership(nil), groups...)
	if err := r.publish(ctx, txClient, record.ID, groupIDs); err != nil {
		return err
	}

	if tx != nil {
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (r *ProviderStore) GetByID(ctx context.Context, id int64) (*provider.Record, error) {
	m, err := r.client.Provider.Query().Where(dbprovider.IDEQ(id)).Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, provider.ErrProviderNotFound, nil)
	}

	providers, err := r.RecordsFromEntities(ctx, []*dbent.Provider{m})
	if err != nil {
		return nil, err
	}
	if len(providers) == 0 {
		return nil, provider.ErrProviderNotFound
	}
	return &providers[0], nil
}

func (r *ProviderStore) GetByIDs(ctx context.Context, ids []int64) ([]*provider.Record, error) {
	if len(ids) == 0 {
		return []*provider.Record{}, nil
	}

	// De-duplicate while preserving order of first occurrence.
	uniqueIDs := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		uniqueIDs = append(uniqueIDs, id)
	}
	if len(uniqueIDs) == 0 {
		return []*provider.Record{}, nil
	}

	entProviders, err := r.client.Provider.
		Query().
		Where(dbprovider.IDIn(uniqueIDs...)).
		WithProxy().
		All(ctx)
	if err != nil {
		return nil, err
	}
	if len(entProviders) == 0 {
		return []*provider.Record{}, nil
	}

	providerIDs := make([]int64, 0, len(entProviders))
	entByID := make(map[int64]*dbent.Provider, len(entProviders))
	for _, acc := range entProviders {
		entByID[acc.ID] = acc
		providerIDs = append(providerIDs, acc.ID)
	}

	groupsByProvider, groupIDsByProvider, providerGroupsByProvider, err := r.LoadProviderGroups(ctx, providerIDs)
	if err != nil {
		return nil, err
	}

	outByID := make(map[int64]*provider.Record, len(entProviders))
	for _, entAcc := range entProviders {
		out := r.recordFromEntity(entAcc)
		if out == nil {
			continue
		}

		// Prefer the preloaded proxy edge when available.
		if entAcc.Edges.Proxy != nil {
			out.Proxy = r.options.Proxy(entAcc.Edges.Proxy)
		}

		if groups, ok := groupsByProvider[entAcc.ID]; ok {
			out.Groups = groups
		}
		if groupIDs, ok := groupIDsByProvider[entAcc.ID]; ok {
			out.GroupIDs = groupIDs
		}
		if ags, ok := providerGroupsByProvider[entAcc.ID]; ok {
			out.ProviderGroups = ags
		}
		outByID[entAcc.ID] = out
	}

	// Preserve input order (first occurrence), and ignore missing IDs.
	out := make([]*provider.Record, 0, len(uniqueIDs))
	for _, id := range uniqueIDs {
		if _, ok := entByID[id]; !ok {
			continue
		}
		if acc, ok := outByID[id]; ok && acc != nil {
			out = append(out, provider.CloneRecord(acc))
		}
	}

	return out, nil
}

func (r *ProviderStore) RecordsFromEntities(ctx context.Context, providers []*dbent.Provider) ([]provider.Record, error) {
	if len(providers) == 0 {
		return []provider.Record{}, nil
	}

	providerIDs := make([]int64, 0, len(providers))
	proxyIDs := make([]int64, 0, len(providers))
	for _, acc := range providers {
		providerIDs = append(providerIDs, acc.ID)
		if acc.ProxyID != nil {
			proxyIDs = append(proxyIDs, *acc.ProxyID)
		}
		if acc.ProxyFallbackOriginID != nil {
			proxyIDs = append(proxyIDs, *acc.ProxyFallbackOriginID)
		}
	}

	proxyMap, err := r.LoadProxies(ctx, proxyIDs)
	if err != nil {
		return nil, err
	}
	groupsByProvider, groupIDsByProvider, providerGroupsByProvider, err := r.LoadProviderGroups(ctx, providerIDs)
	if err != nil {
		return nil, err
	}

	outProviders := make([]provider.Record, 0, len(providers))
	for _, acc := range providers {
		out := r.recordFromEntity(acc)
		if out == nil {
			continue
		}
		if acc.ProxyID != nil {
			if proxy, ok := proxyMap[*acc.ProxyID]; ok {
				out.Proxy = proxy
			}
		}
		out.ProxyFallbackOriginID = acc.ProxyFallbackOriginID
		if acc.ProxyFallbackOriginID != nil {
			if op, ok := proxyMap[*acc.ProxyFallbackOriginID]; ok && op != nil {
				n := op.Name
				out.ProxyFallbackOriginName = &n
			}
		}
		if groups, ok := groupsByProvider[acc.ID]; ok {
			out.Groups = groups
		}
		if groupIDs, ok := groupIDsByProvider[acc.ID]; ok {
			out.GroupIDs = groupIDs
		}
		if ags, ok := providerGroupsByProvider[acc.ID]; ok {
			out.ProviderGroups = ags
		}
		outProviders = append(outProviders, *provider.CloneRecord(out))
	}

	return outProviders, nil
}

func (r *ProviderStore) LoadProxies(ctx context.Context, proxyIDs []int64) (map[int64]*egress.Proxy, error) {
	proxyMap := make(map[int64]*egress.Proxy)
	proxyIDs = UniquePositiveInt64s(proxyIDs)
	if len(proxyIDs) == 0 {
		return proxyMap, nil
	}

	for start := 0; start < len(proxyIDs); start += postgresParameterBatchSize {
		end := min(start+postgresParameterBatchSize, len(proxyIDs))
		proxies, err := r.client.Proxy.Query().Where(dbproxy.IDIn(proxyIDs[start:end]...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range proxies {
			proxyMap[p.ID] = r.options.Proxy(p)
		}
	}
	return proxyMap, nil
}

func (r *ProviderStore) LoadProviderGroups(ctx context.Context, providerIDs []int64) (map[int64][]*accessview.GroupConfig, map[int64][]int64, map[int64][]provider.GroupMembership, error) {
	groupsByProvider := make(map[int64][]*accessview.GroupConfig)
	groupIDsByProvider := make(map[int64][]int64)
	providerGroupsByProvider := make(map[int64][]provider.GroupMembership)

	providerIDs = UniquePositiveInt64s(providerIDs)
	if len(providerIDs) == 0 {
		return groupsByProvider, groupIDsByProvider, providerGroupsByProvider, nil
	}

	for start := 0; start < len(providerIDs); start += postgresParameterBatchSize {
		end := min(start+postgresParameterBatchSize, len(providerIDs))
		entries, err := r.client.ProviderGroup.Query().
			Where(dbprovidergroup.ProviderIDIn(providerIDs[start:end]...)).
			Order(dbprovidergroup.ByProviderID(), dbprovidergroup.ByGroupID()).
			All(ctx)
		if err != nil {
			return nil, nil, nil, err
		}

		groupIDs := make([]int64, 0, len(entries))
		for _, ag := range entries {
			groupIDs = append(groupIDs, ag.GroupID)
		}
		groupMap, err := r.LoadGroups(ctx, groupIDs)
		if err != nil {
			return nil, nil, nil, err
		}

		for _, ag := range entries {
			groupSvc := groupMap[ag.GroupID]
			agSvc := provider.GroupMembership{
				ProviderID: ag.ProviderID,
				GroupID:    ag.GroupID,
				CreatedAt:  ag.CreatedAt,
				Group:      groupSvc,
			}
			providerGroupsByProvider[ag.ProviderID] = append(providerGroupsByProvider[ag.ProviderID], agSvc)
			groupIDsByProvider[ag.ProviderID] = append(groupIDsByProvider[ag.ProviderID], ag.GroupID)
			if groupSvc != nil {
				groupsByProvider[ag.ProviderID] = append(groupsByProvider[ag.ProviderID], groupSvc)
			}
		}
	}

	return groupsByProvider, groupIDsByProvider, providerGroupsByProvider, nil
}

func (r *ProviderStore) LoadGroups(ctx context.Context, groupIDs []int64) (map[int64]*accessview.GroupConfig, error) {
	groupMap := make(map[int64]*accessview.GroupConfig)
	groupIDs = UniquePositiveInt64s(groupIDs)
	if len(groupIDs) == 0 {
		return groupMap, nil
	}

	for start := 0; start < len(groupIDs); start += postgresParameterBatchSize {
		end := min(start+postgresParameterBatchSize, len(groupIDs))
		groups, err := r.client.Group.Query().Where(dbgroup.IDIn(groupIDs[start:end]...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, g := range groups {
			groupMap[g.ID] = r.options.Group(g)
		}
	}
	return groupMap, nil
}

func UniquePositiveInt64s(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func RecordFromEntity(m *dbent.Provider) *provider.Record {
	if m == nil {
		return nil
	}

	rateMultiplier := m.RateMultiplier

	return &provider.Record{
		ID:                      m.ID,
		Name:                    m.Name,
		Notes:                   m.Notes,
		Platform:                m.Platform,
		Type:                    m.Type,
		Credentials:             provider.CloneValues(m.Credentials),
		Extra:                   provider.CloneValues(m.Extra),
		ProxyID:                 m.ProxyID,
		ProxyFallbackOriginID:   m.ProxyFallbackOriginID,
		Concurrency:             m.Concurrency,
		Priority:                m.Priority,
		RateMultiplier:          &rateMultiplier,
		LoadFactor:              m.LoadFactor,
		Status:                  m.Status,
		ErrorMessage:            derefString(m.ErrorMessage),
		LastUsedAt:              m.LastUsedAt,
		ExpiresAt:               m.ExpiresAt,
		AutoPauseOnExpired:      m.AutoPauseOnExpired,
		CreatedAt:               m.CreatedAt,
		UpdatedAt:               m.UpdatedAt,
		Schedulable:             m.Schedulable,
		RateLimitedAt:           m.RateLimitedAt,
		RateLimitResetAt:        m.RateLimitResetAt,
		OverloadUntil:           m.OverloadUntil,
		TempUnschedulableUntil:  m.TempUnschedulableUntil,
		TempUnschedulableReason: derefString(m.TempUnschedulableReason),
		SessionWindowStart:      m.SessionWindowStart,
		SessionWindowEnd:        m.SessionWindowEnd,
		SessionWindowStatus:     derefString(m.SessionWindowStatus),
		ParentProviderID:        m.ParentProviderID,
		QuotaDimension:          string(m.QuotaDimension),
	}
}

// translatePersistenceError maps not-found and unique-constraint failures to the supplied application errors, retaining the database cause.
// A nil target or an unmatched error type leaves the input error unchanged.
func translatePersistenceError(err error, notFound, conflict *infraerrors.ApplicationError) error {
	if err == nil {
		return nil
	}

	// Accept both Ent's NotFoundError and database/sql's ErrNoRows.

	if notFound != nil && (errors.Is(err, sql.ErrNoRows) || dbent.IsNotFound(err)) {
		return notFound.WithCause(err)
	}

	if conflict != nil && postgresinfra.IsUniqueConstraintViolation(err) {
		return conflict.WithCause(err)
	}

	return err
}
