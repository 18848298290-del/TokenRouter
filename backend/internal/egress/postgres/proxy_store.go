package postgres

import (
	"context"
	"database/sql"
	"slices"
	"sort"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/proxy"
	"github.com/TokenFlux/TokenRouter/internal/egress"
	postgresinfra "github.com/TokenFlux/TokenRouter/internal/infra/postgres"
	logger "github.com/TokenFlux/TokenRouter/internal/infra/telemetry/logging"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
)

// Proxy connection changes can invalidate stored Ollama usage observations
// attached to providers that use that proxy. The storage adapter locks the
// proxy connection identity, writes the proxy, updates affected provider
// snapshots, and enqueues their scheduler changes using the same executor when
// it owns or joins the write transaction.
//
// Expiration sweeps take a proxy snapshot first and process each expired active
// proxy in a separate transaction. Retained bindings publish snapshot changes
// inside that transaction, while reassigned-provider IDs are aggregated and
// published afterward. Earlier transactions can remain committed if a later
// sweep fails, and the final aggregated event is skipped by that early return.
// Repository listing separately supports ordinary field order and whole-result
// provider-count order before pagination.

// proxyProviderOutboxChunkSize bounds provider-ID payload slices enqueued by
// connection-update and retained-binding snapshot invalidation within their
// executor context.
const proxyProviderOutboxChunkSize = 500

// ProxyProviderParticipant changes provider proxy associations and snapshots on
// the supplied executor owned by egress.
type ProxyProviderParticipant interface {
	// InvalidateSnapshots removes eligible stored Ollama observations for
	// providers bound to the proxy and returns affected IDs. The caller owns
	// transaction completion and publishing those returned changes through its
	// executor.
	InvalidateSnapshots(context.Context, int64) ([]int64, error)
	// Reassign changes eligible provider bindings from proxyID, retaining their
	// original proxy identity for restoration, and returns changed IDs. A nil
	// target selects direct connections; the caller owns commit and event
	// publication.
	Reassign(context.Context, int64, *int64) ([]int64, error)
}

// ProxyStoreOptions binds provider participation and scheduler outbox writes to
// each egress-selected SQL executor.
//
// Providers and Enqueue use the executor supplied by each write phase.
// Transactional proxy operations pass their transaction client or executor so
// provider changes and outbox writes share its completion rules.
type ProxyStoreOptions struct {
	// Providers needs to return a participant using the supplied executor,
	// including transaction-bound clients. The store calls it during connection
	// invalidation and expiration reassignment.
	// executor-bound provider participant factory
	Providers func(postgresinfra.Executor) ProxyProviderParticipant
	// Enqueue writes scheduler changes through the supplied executor and consumes
	// each payload synchronously before returning. Connection-update events share
	// transaction completion; final reassignment-sweep aggregation uses the
	// store's sql handle afterward.
	// executor-bound scheduler change writer
	Enqueue func(context.Context, postgresinfra.Executor, any) error
}

// ProxyStore persists proxy values and coordinates provider changes through its
// selected SQL executor and transaction client.
//
// client performs proxy persistence and transaction selection, while sql
// supplies raw provider counts and final sweep publication. A
// transaction-bound client also needs sql bound to that caller-owned
// transaction for the ErrTxStarted sweep path.
type ProxyStore struct {
	changes ProxyStoreOptions      // provider and outbox operation bindings
	client  *dbent.Client          // borrowed proxy persistence Ent client
	sql     postgresinfra.Executor // borrowed raw SQL executor
}

// ProxyConnectionIdentity aliases the egress connection/status value compared
// during stored proxy updates.
type ProxyConnectionIdentity = egress.ProxyConnectionIdentity

// NewProxyStore constructs persistence with an Ent client, SQL executor, and
// provider/outbox bindings. The caller keeps handles available for repository
// operations and supplies a transaction-matching executor when using a
// transaction-bound Ent client.
func NewProxyStore(client *dbent.Client, db postgresinfra.Executor, options ProxyStoreOptions) *ProxyStore {
	return &ProxyStore{client: client, sql: db, changes: options}
}

// Create persists a proxy and copies its ID and creation/update timestamps into
// the supplied model after success. Optional empty credentials and nil
// expiration/backup values are omitted from the builder; failure leaves those
// generated metadata assignments unapplied.
func (r *ProxyStore) Create(ctx context.Context, proxyIn *egress.Proxy) error {
	builder := r.client.Proxy.Create().
		SetName(proxyIn.Name).
		SetProtocol(proxyIn.Protocol).
		SetHost(proxyIn.Host).
		SetPort(proxyIn.Port).
		SetStatus(proxyIn.Status).
		SetFallbackMode(proxyIn.FallbackMode).
		SetExpiryWarnDays(proxyIn.ExpiryWarnDays)
	if proxyIn.Username != "" {
		builder.SetUsername(proxyIn.Username)
	}
	if proxyIn.Password != "" {
		builder.SetPassword(proxyIn.Password)
	}
	if proxyIn.ExpiresAt != nil {
		builder.SetExpiresAt(*proxyIn.ExpiresAt)
	}
	if proxyIn.BackupProxyID != nil {
		builder.SetBackupProxyID(*proxyIn.BackupProxyID)
	}

	created, err := builder.Save(ctx)
	if err == nil {
		applyProxyEntityToService(proxyIn, created)
	}
	return err
}

// GetByID returns the mapped persisted proxy or ErrProxyNotFound when Ent
// reports its ID absent. Mapping copies scalar values but retains optional
// expiration and backup pointers from the loaded entity.
func (r *ProxyStore) GetByID(ctx context.Context, id int64) (*egress.Proxy, error) {
	m, err := r.client.Proxy.Get(ctx, id)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, egress.ErrProxyNotFound
		}
		return nil, err
	}
	return ProxyEntity(m), nil
}

// ListByIDs returns records matching the selected IDs in repository query
// order. Empty input returns a present empty slice, and repeated selected IDs
// do not request repeated output rows from the IDIn query.
func (r *ProxyStore) ListByIDs(ctx context.Context, ids []int64) ([]egress.Proxy, error) {
	if len(ids) == 0 {
		return []egress.Proxy{}, nil
	}

	proxies, err := r.client.Proxy.Query().
		Where(proxy.IDIn(ids...)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]egress.Proxy, 0, len(proxies))
	for i := range proxies {
		out = append(out, *ProxyEntity(proxies[i]))
	}
	return out, nil
}

// Update replaces the stored proxy and coordinates connection-dependent provider
// snapshots and outbox writes. It joins an Ent transaction from ctx when
// present; otherwise it opens a transaction unless the client is already
// transaction-bound.
//
// An owned transaction commits before metadata is copied to proxyIn, with
// deferred rollback attempting cleanup of unfinished work. Joined transaction
// completion belongs to the caller, so successful return there does not
// establish commit. The update helper locks the prior identity before comparison
// and performs invalidation/event writes through the selected client, keeping
// those operations on the same transaction when one is available.
func (r *ProxyStore) Update(ctx context.Context, proxyIn *egress.Proxy) error {
	client := r.client
	var tx *dbent.Tx
	if contextTx := dbent.TxFromContext(ctx); contextTx != nil {
		client = contextTx.Client()
	} else {
		var err error
		tx, err = r.client.Tx(ctx)
		if err != nil && err != dbent.ErrTxStarted {
			return err
		}
		// Retain rollback cleanup only for a transaction opened here. Joined
		// transactions remain with their outer caller through provider/outbox work.
		if tx != nil {
			defer func() { _ = tx.Rollback() }()
			ctx = dbent.NewTxContext(ctx, tx)
			client = tx.Client()
		}
	}

	updated, err := r.updateProxyAndInvalidateOllamaSnapshot(ctx, client, proxyIn)
	if err != nil {
		return err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	applyProxyEntityToService(proxyIn, updated)
	return nil
}

// ProxyConnectionIdentityFromProxy extracts the egress connection/status
// comparison value for stored proxy updates.
func ProxyConnectionIdentityFromProxy(proxyIn *egress.Proxy) ProxyConnectionIdentity {
	return egress.ProxyConnectionIdentityFromProxy(proxyIn)
}

// updateProxyAndInvalidateOllamaSnapshot locks prior connection values,
// replaces the proxy, and invalidates eligible Ollama observations when
// identity changed. It receives the selected client and context from the
// transaction owner. Missing rows produce ErrProxyNotFound.
//
// Credentials, expiration, and backup fields are cleared when their supplied
// complete values are empty or nil. Only protocol, host, port, credentials, and
// status participate in connection identity comparison; other metadata changes
// return after the proxy write. Changed provider snapshot IDs are enqueued
// through that same client, leaving commit or rollback to Update or its outer
// caller.
func (r *ProxyStore) updateProxyAndInvalidateOllamaSnapshot(ctx context.Context, client *dbent.Client, proxyIn *egress.Proxy) (*dbent.Proxy, error) {
	currentIdentity, err := lockProxyConnectionIdentity(ctx, client, proxyIn.ID)
	if err != nil {
		return nil, err
	}
	builder := client.Proxy.UpdateOneID(proxyIn.ID).
		SetName(proxyIn.Name).
		SetProtocol(proxyIn.Protocol).
		SetHost(proxyIn.Host).
		SetPort(proxyIn.Port).
		SetStatus(proxyIn.Status).
		SetFallbackMode(proxyIn.FallbackMode).
		SetExpiryWarnDays(proxyIn.ExpiryWarnDays)
	if proxyIn.Username != "" {
		builder.SetUsername(proxyIn.Username)
	} else {
		builder.ClearUsername()
	}
	if proxyIn.Password != "" {
		builder.SetPassword(proxyIn.Password)
	} else {
		builder.ClearPassword()
	}
	if proxyIn.ExpiresAt != nil {
		builder.SetExpiresAt(*proxyIn.ExpiresAt)
	} else {
		builder.ClearExpiresAt()
	}
	if proxyIn.BackupProxyID != nil {
		builder.SetBackupProxyID(*proxyIn.BackupProxyID)
	} else {
		builder.ClearBackupProxyID()
	}

	updated, err := builder.Save(ctx)
	if dbent.IsNotFound(err) {
		return nil, egress.ErrProxyNotFound
	}
	if err != nil {
		return nil, err
	}
	// Compare the locked connection tuple after persistence. Changes outside
	// that tuple retain stored snapshot values in this operation rather than
	// triggering its connection-dependent invalidation and event phase.
	if currentIdentity == ProxyConnectionIdentityFromProxy(proxyIn) {
		return updated, nil
	}
	providerIDs, err := r.changes.Providers(client).InvalidateSnapshots(ctx, proxyIn.ID)
	if err != nil {
		return nil, err
	}
	if err := r.EnqueueProviderChanges(ctx, client, providerIDs); err != nil {
		return nil, err
	}
	return updated, nil
}

// lockProxyConnectionIdentity reads the proxy's connection/status tuple with
// FOR NO KEY UPDATE and normalized null credentials. The caller keeps its
// transaction active through subsequent update/comparison; the row lock then
// coordinates competing stored connection changes.
func lockProxyConnectionIdentity(ctx context.Context, client *dbent.Client, proxyID int64) (ProxyConnectionIdentity, error) {
	rows, err := client.QueryContext(ctx, `
		SELECT protocol, host, port, COALESCE(username, ''), COALESCE(password, ''), status
		FROM proxies
		WHERE id = $1 AND deleted_at IS NULL
		FOR NO KEY UPDATE
	`, proxyID)
	if err != nil {
		return ProxyConnectionIdentity{}, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return ProxyConnectionIdentity{}, err
		}
		return ProxyConnectionIdentity{}, egress.ErrProxyNotFound
	}
	var identity ProxyConnectionIdentity
	if err := rows.Scan(&identity.Protocol, &identity.Host, &identity.Port, &identity.Username, &identity.Password, &identity.Status); err != nil {
		return ProxyConnectionIdentity{}, err
	}
	return identity, rows.Err()
}

// EnqueueProviderChanges sorts and compacts providerIDs in place, then enqueues
// bounded slices through exec. Callers retain that input storage throughout
// synchronous enqueueing and copy it first if their ordering or full contents
// must remain independent.
//
// Chunks are issued sequentially, and a later error can follow earlier
// successful enqueue calls. The executor owner determines commit and rollback,
// so transactional chunks remain pending its completion while separately
// committed writes can survive a later failure. The bound writer consumes each
// payload before return, allowing the next chunk to reuse the same provider-ID
// backing slice safely.
func (r *ProxyStore) EnqueueProviderChanges(ctx context.Context, exec postgresinfra.Executor, providerIDs []int64) error {
	// Compact identity duplicates before chunking so one update's snapshot
	// changes enqueue each provider ID once across these bounded payloads.
	providerIDs = SortedUniqueProviderIDs(providerIDs)
	for start := 0; start < len(providerIDs); start += proxyProviderOutboxChunkSize {
		end := min(start+proxyProviderOutboxChunkSize, len(providerIDs))
		payload := map[string]any{"provider_ids": providerIDs[start:end]}
		if err := r.changes.Enqueue(ctx, exec, payload); err != nil {
			return err
		}
	}
	return nil
}

// Delete executes repository deletion for id and succeeds when no matching row
// is found. Bound-provider rejection belongs to ProxyAdmin before this method;
// the adapter does not perform that count check.
func (r *ProxyStore) Delete(ctx context.Context, id int64) error {
	_, err := r.client.Proxy.Delete().Where(proxy.IDEQ(id)).Exec(ctx)
	return err
}

// List delegates proxy pagination with no protocol, status, or search filters.
func (r *ProxyStore) List(ctx context.Context, params pagination.PaginationParams) ([]egress.Proxy, *pagination.PaginationResult, error) {
	return r.ListWithFilters(ctx, params, "", "", "")
}

// ListWithFilters returns a page and total matching exact protocol/status
// filters and case-insensitive name search. Ordering uses normalized supported
// fields with an ID tie-breaker; count and row retrieval are separate queries
// and can observe concurrent changes.
func (r *ProxyStore) ListWithFilters(ctx context.Context, params pagination.PaginationParams, protocol, status, search string) ([]egress.Proxy, *pagination.PaginationResult, error) {
	q := r.client.Proxy.Query()
	if protocol != "" {
		q = q.Where(proxy.ProtocolEQ(protocol))
	}
	if status != "" {
		q = q.Where(proxy.StatusEQ(status))
	}
	if search != "" {
		q = q.Where(proxy.NameContainsFold(search))
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	proxiesQuery := q.
		Offset(params.Offset()).
		Limit(params.Limit())
	for _, order := range proxyListOrder(params) {
		proxiesQuery = proxiesQuery.Order(order)
	}

	proxies, err := proxiesQuery.All(ctx)
	if err != nil {
		return nil, nil, err
	}

	outProxies := make([]egress.Proxy, 0, len(proxies))
	for i := range proxies {
		outProxies = append(outProxies, *ProxyEntity(proxies[i]))
	}

	return outProxies, pagination.ResultFromTotal(int64(total), params), nil
}

// ListWithFiltersAndProviderCount returns matching proxies with nondeleted
// bound-provider counts and pagination metadata. Ordinary field ordering pages
// the proxy query before augmentation; provider_count ordering instead loads
// and sorts all matching rows before selecting the page.
func (r *ProxyStore) ListWithFiltersAndProviderCount(ctx context.Context, params pagination.PaginationParams, protocol, status, search string) ([]egress.ProxyWithProviderCount, *pagination.PaginationResult, error) {
	q := r.client.Proxy.Query()
	if protocol != "" {
		q = q.Where(proxy.ProtocolEQ(protocol))
	}
	if status != "" {
		q = q.Where(proxy.StatusEQ(status))
	}
	if search != "" {
		q = q.Where(proxy.NameContainsFold(search))
	}

	total, err := q.Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	if strings.EqualFold(strings.TrimSpace(params.SortBy), "provider_count") {
		return r.listWithProviderCountSort(ctx, q, params, total)
	}

	proxiesQuery := q.
		Offset(params.Offset()).
		Limit(params.Limit())
	for _, order := range proxyListOrder(params) {
		proxiesQuery = proxiesQuery.Order(order)
	}

	proxies, err := proxiesQuery.All(ctx)
	if err != nil {
		return nil, nil, err
	}

	return r.buildProxyWithProviderCountResult(ctx, proxies, params, int64(total))
}

// listWithProviderCountSort obtains all matching proxies, augments counts, and
// sorts them before slicing the requested page. Provider count follows
// normalized requested direction, and equal counts use decreasing proxy ID.
//
// The initial query and later grouped count read are separate operations, so
// this result describes their sampled values rather than a transaction
// snapshot. Sorting the complete set keeps provider-count ordering across page
// selection; paging proxies first would exclude candidates whose associated
// counts place them earlier in the requested order.
func (r *ProxyStore) listWithProviderCountSort(ctx context.Context, q *dbent.ProxyQuery, params pagination.PaginationParams, total int) ([]egress.ProxyWithProviderCount, *pagination.PaginationResult, error) {
	proxies, err := q.
		Order(dbent.Desc(proxy.FieldID)).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	result, _, err := r.buildProxyWithProviderCountResult(ctx, proxies, params, int64(total))
	if err != nil {
		return nil, nil, err
	}

	sortOrder := params.NormalizedSortOrder(pagination.SortOrderDesc)
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].ProviderCount == result[j].ProviderCount {
			return result[i].ID > result[j].ID
		}
		if sortOrder == pagination.SortOrderAsc {
			return result[i].ProviderCount < result[j].ProviderCount
		}
		return result[i].ProviderCount > result[j].ProviderCount
	})

	return pagination.Slice(result, params), pagination.ResultFromTotal(int64(total), params), nil
}

// buildProxyWithProviderCountResult reads grouped nondeleted-provider counts
// and maps the supplied proxy entities into augmented values. Missing count
// keys use zero, nil mapped entities are skipped, and supplied total remains
// the pagination count from the earlier query.
func (r *ProxyStore) buildProxyWithProviderCountResult(ctx context.Context, proxies []*dbent.Proxy, params pagination.PaginationParams, total int64) ([]egress.ProxyWithProviderCount, *pagination.PaginationResult, error) {
	counts, err := r.GetProviderCountsForProxies(ctx)
	if err != nil {
		return nil, nil, err
	}

	result := make([]egress.ProxyWithProviderCount, 0, len(proxies))
	for i := range proxies {
		proxyOut := ProxyEntity(proxies[i])
		if proxyOut == nil {
			continue
		}
		result = append(result, egress.ProxyWithProviderCount{
			Proxy:         *proxyOut,
			ProviderCount: counts[proxyOut.ID],
		})
	}

	return result, pagination.ResultFromTotal(total, params), nil
}

// proxyListOrder selects normalized supported proxy fields and an ID
// tie-breaker using the requested direction. Unknown fields use ID and invalid
// direction uses descending; expiry ordering follows PostgreSQL's default null
// placement for the chosen direction.
func proxyListOrder(params pagination.PaginationParams) []func(*entsql.Selector) {
	sortBy := strings.ToLower(strings.TrimSpace(params.SortBy))
	sortOrder := params.NormalizedSortOrder(pagination.SortOrderDesc)

	var field string
	switch sortBy {
	case "name":
		field = proxy.FieldName
	case "protocol":
		field = proxy.FieldProtocol
	case "status":
		field = proxy.FieldStatus
	case "created_at":
		field = proxy.FieldCreatedAt
	case "expiry":
		// A NULL expires_at means no expiration. PostgreSQL's default NULL ordering
		// places these values last in ascending order and first in descending order.
		field = proxy.FieldExpiresAt
	default:
		field = proxy.FieldID
	}

	if sortOrder == pagination.SortOrderAsc {
		return []func(*entsql.Selector){dbent.Asc(field), dbent.Asc(proxy.FieldID)}
	}
	return []func(*entsql.Selector){dbent.Desc(field), dbent.Desc(proxy.FieldID)}
}

// ListActive returns proxies with the stored active status value in repository
// query order. It does not evaluate ExpiresAt, allowing expiration maintenance
// to update status separately after the time threshold is reached.
func (r *ProxyStore) ListActive(ctx context.Context) ([]egress.Proxy, error) {
	proxies, err := r.client.Proxy.Query().
		Where(proxy.StatusEQ(egress.StatusActive)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	outProxies := make([]egress.Proxy, 0, len(proxies))
	for i := range proxies {
		outProxies = append(outProxies, *ProxyEntity(proxies[i]))
	}
	return outProxies, nil
}

// ExistsByHostPortAuth reports whether a stored proxy matches host, port,
// username, and password, independently of protocol. Empty credential text
// matches either null or empty persisted values; other text requires equality.
func (r *ProxyStore) ExistsByHostPortAuth(ctx context.Context, host string, port int, username, password string) (bool, error) {
	q := r.client.Proxy.Query().
		Where(proxy.HostEQ(host), proxy.PortEQ(port))

	if username == "" {
		q = q.Where(proxy.Or(proxy.UsernameIsNil(), proxy.UsernameEQ("")))
	} else {
		q = q.Where(proxy.UsernameEQ(username))
	}
	if password == "" {
		q = q.Where(proxy.Or(proxy.PasswordIsNil(), proxy.PasswordEQ("")))
	} else {
		q = q.Where(proxy.PasswordEQ(password))
	}

	count, err := q.Count(ctx)
	return count > 0, err
}

// CountProvidersByProxyID counts nondeleted providers currently bound to the
// supplied proxy identity.
func (r *ProxyStore) CountProvidersByProxyID(ctx context.Context, proxyID int64) (int64, error) {
	var count int64
	if err := postgresinfra.ScanSingleRow(ctx, r.sql, "SELECT COUNT(*) FROM providers WHERE proxy_id = $1 AND deleted_at IS NULL", []any{proxyID}, &count); err != nil {
		return 0, err
	}
	return count, nil
}

// ListProviderSummariesByProxyID reads nondeleted bound-provider summaries
// ordered by decreasing ID. Nullable notes become optional string pointers, and
// iteration errors discard the accumulated result before it is returned to
// administration.
func (r *ProxyStore) ListProviderSummariesByProxyID(ctx context.Context, proxyID int64) ([]egress.ProxyProviderSummary, error) {
	rows, err := r.sql.QueryContext(ctx, `
		SELECT id, name, platform, type, notes
		FROM providers
		WHERE proxy_id = $1 AND deleted_at IS NULL
		ORDER BY id DESC
	`, proxyID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]egress.ProxyProviderSummary, 0)
	for rows.Next() {
		var (
			id       int64
			name     string
			platform string
			accType  string
			notes    sql.NullString
		)
		if err := rows.Scan(&id, &name, &platform, &accType, &notes); err != nil {
			return nil, err
		}
		var notesPtr *string
		if notes.Valid {
			notesPtr = &notes.String
		}
		out = append(out, egress.ProxyProviderSummary{
			ID:       id,
			Name:     name,
			Platform: platform,
			Type:     accType,
			Notes:    notesPtr,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// GetProviderCountsForProxies returns grouped counts for nondeleted providers
// with nonnull proxy bindings. It consumes and closes the query rows; an
// iteration or final close failure returns no usable count map with its error.
func (r *ProxyStore) GetProviderCountsForProxies(ctx context.Context) (counts map[int64]int64, err error) {
	rows, err := r.sql.QueryContext(ctx, "SELECT proxy_id, COUNT(*) AS count FROM providers WHERE proxy_id IS NOT NULL AND deleted_at IS NULL GROUP BY proxy_id")
	if err != nil {
		return nil, err
	}
	// A successful scan still needs query cleanup to succeed. Surface a final close
	// error and discard its count map when no earlier error already explains
	// failure.
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			counts = nil
		}
	}()

	counts = make(map[int64]int64)
	for rows.Next() {
		var proxyID, count int64
		if err = rows.Scan(&proxyID, &count); err != nil {
			return nil, err
		}
		counts[proxyID] = count
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return counts, nil
}

// ListActiveWithProviderCount returns stored active proxies ordered by
// decreasing creation time with grouped provider counts. Time-based expiration
// is separate from that status predicate, and proxy/count reads do not form one
// transaction snapshot.
func (r *ProxyStore) ListActiveWithProviderCount(ctx context.Context) ([]egress.ProxyWithProviderCount, error) {
	proxies, err := r.client.Proxy.Query().
		Where(proxy.StatusEQ(egress.StatusActive)).
		Order(dbent.Desc(proxy.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	counts, err := r.GetProviderCountsForProxies(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]egress.ProxyWithProviderCount, 0, len(proxies))
	for i := range proxies {
		proxyOut := ProxyEntity(proxies[i])
		if proxyOut == nil {
			continue
		}
		result = append(result, egress.ProxyWithProviderCount{
			Proxy:         *proxyOut,
			ProviderCount: counts[proxyOut.ID],
		})
	}

	return result, nil
}

// ProxyEntity maps scalar proxy values and retains optional expiration/backup
// pointers from the Ent entity. Nil input returns nil; credential pointers
// become string values, while consumers treat retained optional values
// according to their entity ownership.
func ProxyEntity(m *dbent.Proxy) *egress.Proxy {
	if m == nil {
		return nil
	}
	out := &egress.Proxy{
		ID:             m.ID,
		Name:           m.Name,
		Protocol:       m.Protocol,
		Host:           m.Host,
		Port:           m.Port,
		Status:         m.Status,
		CreatedAt:      m.CreatedAt,
		UpdatedAt:      m.UpdatedAt,
		ExpiresAt:      m.ExpiresAt,
		FallbackMode:   m.FallbackMode,
		BackupProxyID:  m.BackupProxyID,
		ExpiryWarnDays: m.ExpiryWarnDays,
	}
	if m.Username != nil {
		out.Username = *m.Username
	}
	if m.Password != nil {
		out.Password = *m.Password
	}
	return out
}

// applyProxyEntityToService copies only stored identity and creation/update
// timestamps into dst. Nil destination or source does nothing; other editable
// proxy values remain those supplied to the repository write.
func applyProxyEntityToService(dst *egress.Proxy, src *dbent.Proxy) {
	if dst == nil || src == nil {
		return
	}
	dst.ID = src.ID
	dst.CreatedAt = src.CreatedAt
	dst.UpdatedAt = src.UpdatedAt
}

// ListAllForFallback returns all repository-visible proxy statuses for
// backup-chain resolution. It includes inactive and expired candidates, leaving
// timestamp and stored-expiration checks to ResolveProxyFallbackTarget using
// the caller's observation time.
func (r *ProxyStore) ListAllForFallback(ctx context.Context) ([]egress.Proxy, error) {
	proxies, err := r.client.Proxy.Query().All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]egress.Proxy, 0, len(proxies))
	for i := range proxies {
		out = append(out, *ProxyEntity(proxies[i]))
	}
	return out, nil
}

// SweepExpiredProxies reads a fallback snapshot and marks its expired active
// proxies with configured provider reassignment. Each ordinary proxy operation
// commits separately; the returned count includes provider IDs changed before
// any later reported failure.
//
// Expiration eligibility and backup-chain selection use the initial snapshot,
// with no renewed eligibility check before the status UPDATE. Retained bindings
// invalidate eligible snapshots and enqueue changes inside their proxy
// transaction. Reassignment IDs are accumulated and published after the entire
// loop succeeds, using sql outside those ordinary transactions. Enqueue failure
// is logged, while an earlier sweep error returns before final aggregation and
// can leave prior reassignments committed without that final event.
func (r *ProxyStore) SweepExpiredProxies(ctx context.Context, now time.Time) (int64, error) {
	// Take one candidate snapshot before per-proxy write transactions, allowing
	// chain resolution to use the same observed backup identities throughout this
	// sweep.
	all, err := r.ListAllForFallback(ctx)
	if err != nil {
		return 0, err
	}
	byID := make(map[int64]egress.Proxy, len(all))
	for _, p := range all {
		byID[p.ID] = p
	}

	var totalChanged int64
	allChangedProviderIDs := make([]int64, 0)

	for _, p := range all {
		if p.Status != egress.StatusActive || !p.IsExpired(now) {
			continue
		}

		target, change := egress.ResolveProxyFallbackTarget(p, byID, now)
		if !change && p.FallbackMode == egress.FallbackModeProxy {
			// An unresolved configured backup chain leaves provider bindings in place
			// while the expired proxy status is persisted; retain an operational
			// diagnostic.
			logger.LegacyPrintf("repository.proxy", "[ProxyExpiry] proxy %d expired but fallback chain unresolved (cycle/all-expired); providers kept", p.ID)
		}

		changedProviderIDs, sweepErr := r.sweepOneExpiredProxy(ctx, p.ID, target, change)
		if sweepErr != nil {
			return totalChanged, sweepErr
		}
		totalChanged += int64(len(changedProviderIDs))
		allChangedProviderIDs = append(allChangedProviderIDs, changedProviderIDs...)
	}

	changedProviderIDs := SortedUniqueProviderIDs(allChangedProviderIDs)
	if len(changedProviderIDs) > 0 {
		// Owned per-proxy transactions have committed at this point; a
		// transaction-bound caller still owns commit. Publish accumulated IDs through
		// sql after the loop, so this final event follows the executor completion
		// rules supplied by that caller.
		payload := map[string]any{"provider_ids": changedProviderIDs}
		if err := r.changes.Enqueue(ctx, r.sql, payload); err != nil {
			logger.LegacyPrintf("repository.proxy", "[SchedulerOutbox] enqueue proxy expiry provider changes failed: err=%v", err)
		}
	}
	return totalChanged, nil
}

// SortedUniqueProviderIDs sorts and compacts the supplied slice in place,
// returning its deduplicated prefix. Nil and short slices retain their form;
// callers copy input first if they need independent ordering or preservation of
// the longer contents.
func SortedUniqueProviderIDs(providerIDs []int64) []int64 {
	if len(providerIDs) < 2 {
		return providerIDs
	}
	slices.Sort(providerIDs)
	write := 1
	for _, providerID := range providerIDs[1:] {
		if providerID == providerIDs[write-1] {
			continue
		}
		providerIDs[write] = providerID
		write++
	}
	return providerIDs[:write]
}

// sweepOneExpiredProxy performs one proxy expiration and provider phase in an
// owned transaction when possible. A transaction-bound client returns
// ErrTxStarted; that path uses sql and leaves completion to the outer owner,
// which supplies the matching executor.
//
// Owned work attempts rollback after an operation failure and commits after the
// phase succeeds. Commit errors are returned without proving rollback. The
// resulting provider IDs identify reassignment for later aggregate publication;
// retained-binding snapshot events have already been enqueued inside the same
// executor phase, so its transaction owner determines their durability.
func (r *ProxyStore) sweepOneExpiredProxy(ctx context.Context, proxyID int64, target *int64, change bool) ([]int64, error) {
	// A bound Ent client cannot open another transaction. Use the matching sql
	// executor supplied by the caller so this phase stays inside its outer
	// completion.
	tx, txErr := r.client.Tx(ctx)
	if txErr != nil {
		if txErr != dbent.ErrTxStarted {
			return nil, txErr
		}
		// The outer caller owns both expiration and provider write durability,
		// including whether their returned IDs are published after that transaction
		// commits.
		return r.sweepOneExpiredProxyOnExec(ctx, r.sql, proxyID, target, change)
	}

	// Own this transaction until both expiration and provider work succeed. Attempt
	// rollback when this phase fails; its error is discarded and the original
	// operation error returns. Earlier proxy transactions remain separate completed
	// work.
	var providerIDs []int64
	var err error
	providerIDs, err = r.sweepOneExpiredProxyOnExec(ctx, tx, proxyID, target, change)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, commitErr
	}
	return providerIDs, nil
}

// sweepOneExpiredProxyOnExec marks a proxy expired and then uses the same
// executor for its provider phase. With change=false it invalidates eligible
// stored snapshots, enqueues those IDs transactionally when exec is
// transactional, and returns no reassignment IDs.
//
// With change=true it delegates reassignment and returns changed IDs for the
// caller's later aggregate event. A nil target selects direct association in
// the participant. This helper owns neither transaction creation nor
// completion; operation failure is returned so the owner can decide rollback,
// while the two paths retain different event publication timing.
func (r *ProxyStore) sweepOneExpiredProxyOnExec(ctx context.Context, exec postgresinfra.Executor, proxyID int64, target *int64, change bool) ([]int64, error) {
	if _, err := exec.ExecContext(ctx,
		`UPDATE proxies SET status=$1, updated_at=NOW() WHERE id=$2 AND deleted_at IS NULL`,
		egress.StatusExpired, proxyID); err != nil {
		return nil, err
	}
	if !change {
		providerIDs, err := r.changes.Providers(exec).InvalidateSnapshots(ctx, proxyID)
		if err != nil {
			return nil, err
		}
		if err := r.EnqueueProviderChanges(ctx, exec, providerIDs); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return r.changes.Providers(exec).Reassign(ctx, proxyID, target)
}

// CountExpired counts repository-visible proxies whose persisted status is the
// expired value.
func (r *ProxyStore) CountExpired(ctx context.Context) (int64, error) {
	var c int64
	err := postgresinfra.ScanSingleRow(ctx, r.sql, `SELECT COUNT(*) FROM proxies WHERE status=$1 AND deleted_at IS NULL`, []any{egress.StatusExpired}, &c)
	return c, err
}

// CountExpiringSoon counts active proxies whose future expiration falls within
// their configured warning days from now. The current time is excluded and the
// warning endpoint is included; zero warning days counts no future expiration.
func (r *ProxyStore) CountExpiringSoon(ctx context.Context, now time.Time) (int64, error) {
	var c int64
	err := postgresinfra.ScanSingleRow(ctx, r.sql, `
		SELECT COUNT(*) FROM proxies
		WHERE deleted_at IS NULL AND status=$1 AND expires_at IS NOT NULL
		  AND expires_at > $2 AND expires_at <= $2 + (expiry_warn_days || ' days')::interval`,
		[]any{egress.StatusActive, now}, &c)
	return c, err
}
