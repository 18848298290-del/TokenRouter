package scheduler

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Single and forced buckets can reuse one database query while keeping
// distinct epoch tokens and publication versions. The fixtures record captures
// before source reads and distinguish successful full writes from ID-only
// membership writes, allowing assertions to expose reuse after failed, empty,
// or contended publication.
//
// Lifecycle races are made observable with query and cache barriers: tests can
// retire or reopen a bucket after its token was captured and before
// publication. Some fixtures protect only their recorded counters and
// lifecycle maps; setup fields and borrowed provider objects stay stable
// during asynchronous use. Embedded interfaces supply unimplemented methods
// through nil values, so each test uses the overridden reader/cache subset.
// Outbox fixtures model visible IDs and bounded deletion, while retry tests
// move recorded due times directly instead of waiting through production
// cooldowns.

// Snapshot fixtures use active and disabled status values to choose
// authoritative group lifecycle branches separately from bucket epoch and
// provider payload state.
const (
	StatusActive   = "active"   // authoritative group is active.
	StatusDisabled = "disabled" // authoritative group is inactive.
)

// batchQueryBenchmarkProviderCount retains the benchmark writer's observed
// provider count so its local publication work has a consumed result.
var batchQueryBenchmarkProviderCount int

// batchProviderQueryKey identifies a scripted source query by group, first
// platform, and plural-reader flag.
type batchProviderQueryKey struct {
	groupID  int64  // queried fixture group ID
	platform string // first queried platform identity
	// mixed distinguishes plural readers in the scripted key; it does not assert
	// that the service's bucket mode is mixed.
	mixed bool // plural-platform reader was called
}

// batchProviderQueryResult pairs a scripted provider slice with the error
// returned on that call.
type batchProviderQueryResult struct {
	// providers is copied at the slice level by run, while each fixture object
	// and its nested data retain their input ownership.
	providers []SnapshotProvider // scripted provider result slice
	err       error              // scripted source failure cause
}

// batchProviderQueryRepo records source queries and consumes configured
// per-key results before synthesizing providers.
type batchProviderQueryRepo struct {
	// Schedulable readers are overridden here; GetByID and GetByIDs fall through
	// the nil embedded source, so tests restrict calls to the implemented subset.
	SnapshotProviderSource // unimplemented source method embedding

	// mu protects calls and snapshots the configured results and beforeRun hook.
	// Configure results and beforeRun before asynchronous use; the hook executes
	// after this mutex is released.
	mu      sync.Mutex                                           // protects query observations
	calls   map[batchProviderQueryKey]int                        // source calls per query key
	results map[batchProviderQueryKey][]batchProviderQueryResult // ordered scripted query results
	// beforeRun runs after the call ordinal is recorded, allowing tests to assert
	// token preparation without holding the query fixture's mutex.
	beforeRun func(batchProviderQueryKey) // source observation callback
}

// batchSnapshotWrite records a publication token and copied provider slice for
// bucket assertions.
type batchSnapshotWrite struct {
	token SchedulerBucketWriteToken // observed bucket publication token
	// providers copies the input slice's entries, retaining their provider
	// objects. bucketState copies outer write records but shares each nested
	// provider slice.
	providers []SnapshotProvider // copied publication argument slice
}

// batchSnapshotCache records token capture, lock attempts, publication
// failures, and successful per-bucket writes.
type batchSnapshotCache struct {
	// Rebuild capture, lock, lease, and write methods are implemented here;
	// unrelated cache reads retain the nil embedding and are unavailable to these
	// fixtures.
	SnapshotCache // unimplemented cache method embedding

	// mu protects nextEpoch, captures, captured, locks, lockBusy, lockErrors,
	// setErrors, setAttempts, writes, and versions during operations. Tests
	// prepare injected maps before concurrent use; beforeSet executes outside
	// this lock.
	mu        sync.Mutex        // protects batch cache observations
	nextEpoch int64             // fixture capture sequence counter
	captures  []SchedulerBucket // ordered bucket capture attempts
	// captured supplies the exact token accepted by SetSnapshot; a later capture
	// for the same bucket fences an earlier fixture token.
	captured    map[SchedulerBucket]SchedulerBucketWriteToken // latest token per bucket
	locks       map[SchedulerBucket]int                       // bucket acquisition call counts
	lockBusy    map[SchedulerBucket]bool                      // configured bucket contention flags
	lockErrors  map[SchedulerBucket]error                     // configured acquisition failure causes
	setErrors   map[SchedulerBucket]error                     // configured publication failure causes
	setAttempts map[SchedulerBucket]int                       // all bucket publication attempts
	writes      map[SchedulerBucket][]batchSnapshotWrite      // successful bucket publication records
	versions    map[SchedulerBucket]int                       // successful bucket publication count
	// beforeSet is read before acquiring mu and can inspect or block other
	// fixtures; tests install it before asynchronous publication starts.
	beforeSet func() // pre-publication fixture observation hook
}

// batchSnapshotProviderIDCache adds counted full and ID-only publication paths
// plus post-write failure injection.
type batchSnapshotProviderIDCache struct {
	*batchSnapshotCache // base capture and publication fixture

	// reuseMu protects fullCalls, idOnlyCalls, idOnlyError, fullLateErr, and
	// returnEmpty during writer calls. Tests configure injected failures before
	// invoking the service.
	reuseMu     sync.Mutex                // protects encoding-path observations
	fullCalls   map[SchedulerBucket]int   // full writer calls per bucket
	idOnlyCalls map[SchedulerBucket]int   // ID-only writer calls per bucket
	idOnlyError map[SchedulerBucket]error // ID-only publication failure injection
	// fullLateErr simulates an error after SetSnapshot already succeeded,
	// verifying that encoded IDs are not considered reusable solely because
	// earlier writes occurred.
	fullLateErr map[SchedulerBucket]error // failure after base full write
	// returnEmpty forces a nonnil empty ID slice after successful base
	// publication, testing map presence separately from an unestablished encoding
	// result.
	returnEmpty bool // successful empty encoding result
}

// batchQueryBenchmarkRepo returns borrowed provider fixtures for the
// benchmark's single-platform grouped reader.
type batchQueryBenchmarkRepo struct {
	// Only the grouped single-platform reader is implemented for this benchmark;
	// other source methods still resolve through a nil embedding.
	SnapshotProviderSource // unimplemented benchmark source methods
	// providers is returned directly and can contain nil entries because the
	// benchmark cache consumes only its length, not provider metadata.
	providers []SnapshotProvider // borrowed benchmark provider slice
}

// batchQueryBenchmarkCache supplies constant tokens and successful local
// publication for query-reuse measurements.
type batchQueryBenchmarkCache struct {
	// The benchmark uses local token, lease, and write methods; other cache
	// operations remain unsupported through this nil embedding.
	SnapshotCache // unimplemented benchmark cache methods
}

// bulkEventProviderRepo supplies configured bulk records and inherits the
// batch source's grouped readers.
type bulkEventProviderRepo struct {
	*batchProviderQueryRepo // inherited grouped source fixture
	// providers supplies all configured records to GetByIDs irrespective of
	// requested IDs; its returned slice copy retains the provider objects.
	providers []SnapshotProvider // configured existing bulk records
}

// bulkEventSnapshotCache records immediate provider updates and deletions
// alongside bucket capture observations.
type bulkEventSnapshotCache struct {
	*batchSnapshotCache // inherited bucket publication observations

	// providerMu protects setProviderIDs and deleteProviderIDs independently of
	// the embedded bucket cache's mutex. Accessors copy each ID slice for serial
	// assertions.
	providerMu        sync.Mutex // protects immediate provider writes
	setProviderIDs    []int64    // immediate provider update IDs
	deleteProviderIDs []int64    // immediate provider deletion IDs
}

// fullRebuildLifecycleCache combines lifecycle fencing with scripted capture
// failures, one-shot contention, and watermark records.
type fullRebuildLifecycleCache struct {
	*groupLifecycleTestCache // inherited authority and epoch fixture

	// mu protects captureAttempts, captureErrors, lockBusyOnce, watermark, and
	// watermarkWrites. Epoch and lifecycle data use the explicitly addressed
	// embedded mutexes; setup injections stay stable during operations.
	mu              sync.Mutex        // protects full-discovery observations
	captureAttempts []SchedulerBucket // all bucket capture attempts
	captureErrors   map[string]error  // capture failure by bucket string
	// lockBusyOnce removes a positive entry on its first observed acquisition,
	// allowing a subsequent rebuild attempt to acquire that bucket.
	lockBusyOnce    map[string]bool // one-shot bucket contention injection
	watermark       int64           // published fixture outbox ID
	watermarkWrites []int64         // ordered watermark publication IDs
}

// fullRebuildLifecycleGroupRepo separates discovered active IDs from fresh
// authoritative group records for reconciliation.
type fullRebuildLifecycleGroupRepo struct {
	// Active discovery and fresh Lite reads are overridden here; other group
	// methods remain unsupported through the nil embedded source.
	SnapshotGroupSource // unimplemented group source embedding

	// mu protects activeIDs, activeIDsErr, listActiveErr, fresh, freshErr,
	// activeIDCalls, listActiveCalls, and freshCalls. Returned ID/call slices and
	// group values are copied before unlocking.
	mu              sync.Mutex               // protects discovery and fresh reads
	activeIDs       []int64                  // scripted active discovery IDs
	activeIDsErr    error                    // active-ID discovery failure cause
	listActiveErr   error                    // fallback discovery failure cause
	fresh           map[int64]*SnapshotGroup // authoritative groups keyed by ID
	freshErr        map[int64]error          // fresh authority failure by ID
	activeIDCalls   int                      // lightweight discovery call count
	listActiveCalls int                      // fallback discovery call count
	freshCalls      []int64                  // ordered fresh group query IDs
}

// fullRebuildFallbackGroupRepo tests active-group discovery through ListActive
// when the lightweight reader is absent.
type fullRebuildFallbackGroupRepo struct {
	// This fixture implements ListActive alone and intentionally lacks
	// ListActiveIDs; inherited group lookup methods remain unavailable through
	// its nil source.
	SnapshotGroupSource // unimplemented group source methods

	// mu protects groups, err, and listCalls during ListActive; tests configure
	// the group values before concurrent service work uses the fixture.
	mu        sync.Mutex      // protects fallback discovery records
	groups    []SnapshotGroup // scripted active group values
	err       error           // fallback active-read failure cause
	listCalls int             // fallback discovery call count
}

// fullRebuildProviderCall records the group and representative platform of a
// provider query.
type fullRebuildProviderCall struct {
	groupID  int64  // queried scheduling group ID
	platform string // representative queried platform identity
}

// fullRebuildProviderRepo counts provider reads and runs a first-read
// observation hook once.
type fullRebuildProviderRepo struct {
	// Schedulable reader variants record calls directly; unsupported identity
	// readers still fall through this nil embedded source.
	SnapshotProviderSource // unimplemented provider source methods

	// mu protects calls and captures beforeFirst for each read. once executes
	// that hook outside the mutex so it can inspect cache state without nested
	// fixture locking.
	mu    sync.Mutex                // protects recorded provider reads
	calls []fullRebuildProviderCall // ordered provider query records
	// beforeFirst is configured before asynchronous source use and executed at
	// most once through once; later reads still record their group/platform
	// calls.
	beforeFirst func()    // first-read observation callback
	once        sync.Once // first-read callback execution guard
}

// schedulerFullRebuildTestCache counts discovery and capture attempts while
// denying bucket locks for coalescing tests.
type schedulerFullRebuildTestCache struct {
	// Discovery, token capture, and permanently busy bucket acquisition are
	// overridden; unused cache methods remain unsupported through this nil
	// interface.
	SnapshotCache // unimplemented discovery cache methods

	// mu protects listErr, listCalls, captures, and lockCalls in the instrumented
	// methods; tests configure listErr before launching coalesced calls.
	mu        sync.Mutex // protects full-rebuild call counts
	listErr   error      // scripted registry failure cause
	listCalls int        // registry discovery call count
	captures  int        // canonical token capture count
	lockCalls int        // denied bucket acquisition count
}

// groupLifecycleTestCache records global lifecycle ownership, mutation
// ordering, cleanup context, and injected failures.
type groupLifecycleTestCache struct {
	*retirementRaceCache // inherited epoch fencing observations

	// stateMu protects the named owner, mutation, registry, and
	// bucket-observation groups below. Delegated epoch operations use
	// retirementRaceCache.mu after this observation lock is released.
	stateMu sync.Mutex // protects lifecycle fixture observations

	// leaseHeld, lease, leaseSequence, leaseBusy, leaseAcquireErr, and
	// leaseReleaseErr model one global fixture owner under stateMu. They do not
	// create separate distributed group leases. Successful matching release
	// clears held; injected release failure leaves it held.
	leaseHeld       bool                         // global lifecycle owner is held
	lease           SchedulerGroupLifecycleLease // current global fixture owner
	leaseSequence   int                          // synthetic owner sequence counter
	leaseBusy       bool                         // configured owner contention flag
	leaseAcquireErr error                        // configured owner acquisition error
	leaseReleaseErr error                        // configured owner release error
	// acquireCalls, releaseCalls, acquireTTL, acquireDeadline, releaseDeadline,
	// and releaseCtxErr record attempted owner operations under stateMu. The
	// context observations describe supplied budgets and cancellation separately
	// from whether those operations succeed.
	acquireCalls    int           // lifecycle owner acquisition count
	releaseCalls    int           // lifecycle owner release count
	acquireTTL      time.Duration // time.Duration
	acquireDeadline bool          // acquisition context has deadline
	releaseDeadline bool          // release context has deadline
	releaseCtxErr   error         // observed release cancellation cause

	// listErr and listCalls are the lifecycle fixture's registry error and
	// observation count protected by stateMu. Epoch registry identities still
	// come from the nested race cache.
	listErr   error // configured registry failure cause
	listCalls int   // registry read call count

	// retireCalls, retireHeld, reopenHeld, and reopenTokens record mutation
	// attempts and successful tokens under stateMu. retireErr, retireErrAt,
	// reopenErr, and reopenErrAt configure failures by the corresponding absolute
	// recorded ordinal.
	retireCalls  []SchedulerBucket           // all retirement call bucket identities
	reopenTokens []SchedulerBucketWriteToken // successfully reopened publication tokens
	retireHeld   []bool                      // owner state at each retirement
	reopenHeld   []bool                      // owner state at each reopen
	// retireErr and retireErrAt inject all retirement failures when the ordinal
	// is nonpositive, or one absolute recorded call when positive. reopenErr and
	// reopenErrAt use the corresponding reopen ordinal rule.
	retireErr   error // configured retirement failure cause
	retireErrAt int   // one-based retirement failure ordinal
	reopenErr   error // configured reopen failure cause
	reopenErrAt int   // one-based reopen failure ordinal

	// bucketLockBusy, bucketLockErr, bucketLockTTLs, unlockCalls, and setErr
	// supply bucket-operation faults and observations under stateMu. Acquisition
	// errors precede busy status; setErr can stop before nested epoch
	// publication.
	bucketLockBusy bool            // configured bucket contention flag
	bucketLockErr  error           // configured bucket acquisition error
	bucketLockTTLs []time.Duration // observed bucket lease durations
	unlockCalls    int             // bucket lease release count
	setErr         error           // publication error before epoch check
}

// groupLifecycleTestGroupRepo returns one configurable authoritative group
// value with an optional after-read hook.
type groupLifecycleTestGroupRepo struct {
	// Only the configurable Lite authority reader is supported here; discovery
	// and other group reads retain the nil embedded source.
	SnapshotGroupSource // unimplemented group lookup methods

	// mu protects group, err, calls, and the captured afterGet hook while
	// GetByIDLite copies the configured value. set replaces group/err together
	// for later reads.
	mu sync.Mutex // protects configurable group authority
	// group is borrowed while configured and copied by value on successful Lite
	// reads; the requested ID is intentionally ignored so mismatch cases can be
	// injected.
	group *SnapshotGroup // configured authority group pointer
	err   error          // configured authority read error
	calls int            // Lite authority read count
	// afterGet executes after the group value was copied and the mutex released.
	// Read errors or missing groups return before invoking this hook.
	afterGet func() // successful authority-read hook
}

// groupLifecycleTestProviderRepo counts provider reads and can expose a
// cancellation-aware query barrier.
type groupLifecycleTestProviderRepo struct {
	// Grouped single/plural readers are implemented for lifecycle rebuilds; other
	// provider methods remain unsupported through the nil embedding.
	SnapshotProviderSource // unimplemented provider source methods

	// mu protects calls, callsByPlatform and captures err, started, and release
	// before barrier work. beforeLoad is configured before asynchronous use and
	// executes outside this mutex.
	mu              sync.Mutex     // protects source call observations
	calls           int            // all lifecycle provider read counts
	callsByPlatform map[string]int // source calls by representative platform
	err             error          // source error after barrier work
	// started, release, and once expose query progress: once closes started at
	// most once, then a nonnil release blocks each read until opened or its
	// context is selected.
	started chan struct{} // first query started signal
	release chan struct{} // query continuation barrier channel
	once    sync.Once     // query-start signal close guard
	// beforeLoad and beforeLoadOnce execute a configured observation once after
	// the start signal and before the release barrier, allowing current cache
	// state to be asserted.
	beforeLoad     func()    // first-load observation callback
	beforeLoadOnce sync.Once // first-load observation execution guard
}

// outboxCleanupCache stores serial watermark and discovery observations for
// outbox recovery fixtures.
type outboxCleanupCache struct {
	// watermark and setWatermarks are serial fixture state with no base-cache
	// mutex. Tests keep them stable across asynchronous work or arrange the
	// needed read/write ordering outside this fixture.
	watermark       int64             // current serial consumption ID
	setWatermarks   []int64           // ordered serial watermark writes
	updateErr       error             // usage update failure injection
	listBucketErr   error             // registry read failure injection
	listBuckets     []SchedulerBucket // borrowed registry bucket slice
	listBucketCalls int               // serial registry read count
}

// outboxCleanupDeleteCall records the consumed watermark and requested
// deletion chunk size.
type outboxCleanupDeleteCall struct {
	watermark int64 // consumed IDs upper bound
	limit     int   // requested deletion chunk size
}

// outboxCleanupRepo models visible event order, pending IDs, cleanup
// ownership, and chunk deletion.
type outboxCleanupRepo struct {
	// events supplies visible poll order and creation times, while rows supplies
	// MaxID and deletions. The fixture does not derive either collection from the
	// other and has no internal synchronization.
	events              []SchedulerOutboxEvent    // visible event order fixture
	rows                []int64                   // retained event ID fixtures
	maxIDCalls          int                       // maximum-ID read call count
	maxIDErr            error                     // maximum-ID failure injection
	lockAcquired        bool                      // cleanup ownership will be granted
	lockAttempts        int                       // cleanup owner acquisition count
	releaseCount        int                       // cleanup owner release count
	deleteCalls         []outboxCleanupDeleteCall // ordered consumed deletion requests
	firstCreatedAfterID []int64                   // pending-age query watermark IDs
}

// outboxCleanupProviderRepo supplies an empty ungrouped provider result for
// cleanup-only fixture paths.
type outboxCleanupProviderRepo struct {
	// This fixture implements only an empty ungrouped reader. Current health
	// tests omit the group source and fail discovery before provider queries;
	// group-zero queries also bypass this source.
	SnapshotProviderSource // unimplemented provider source embedding
}

// blockingOutboxCleanupCache blocks the first registry read to expose an
// in-flight degradation rebuild.
type blockingOutboxCleanupCache struct {
	*outboxCleanupCache // inherited serial outbox cache state
	// mu protects calls only. started and release are configured channels; the
	// first ListBuckets signal/wait occurs outside this lock, while inherited
	// registry values remain stable.
	mu      sync.Mutex    // protects blocking discovery count
	calls   int           // blocking registry read count
	started chan struct{} // first registry read signal
	// release must be opened by the test to finish the first registry read; this
	// barrier deliberately does not observe context cancellation.
	release chan struct{} // first registry read continuation
}

// outboxCleanupLease runs an optional fixture callback when cleanup ownership
// is released.
type outboxCleanupLease struct {
	// release may be nil, and each Release invokes it again when present. This
	// simple fixture lease itself supplies no idempotence guard.
	release func() // fixture cleanup completion callback
}

// retirementRaceCache models epoch retirement and fenced publication with
// lock-protected observation maps.
type retirementRaceCache struct {
	// Snapshot miss, epoch publication, retirement/reopen, and always-granted
	// bucket leases are implemented; unrelated cache methods remain unsupported
	// through this nil interface.
	SnapshotCache // unimplemented cache method embedding

	// mu protects epochs, retired, captures, reopens, setAttempts, published, and
	// versions. listBuckets and beforeSet are configured before concurrent use;
	// the hook runs before this lock.
	mu          sync.Mutex        // protects bucket epoch observations
	epochs      map[string]int64  // lifecycle epochs by bucket string
	retired     map[string]bool   // retirement flags by bucket string
	listBuckets []SchedulerBucket // borrowed initial registry identities
	captures    []SchedulerBucket // ordered token capture attempts
	reopens     []SchedulerBucket // ordered successful bucket reopens
	setAttempts map[string]int    // all bucket publication attempts
	published   map[string]int    // accepted bucket membership writes
	versions    map[string]int    // accepted bucket version count
	// beforeSet lets a test retire or reopen buckets before SetSnapshot checks
	// epoch. Configure it before asynchronous use; it executes without holding
	// the epoch mutex.
	beforeSet func() // pre-fence publication test hook
}

// retirementGroupRepo supplies borrowed active-group discovery fixtures or
// their configured read error.
type retirementGroupRepo struct {
	// This fixture implements ListActive only for discovery; Lite authority reads
	// must be supplied by another fixture when lifecycle work is expected.
	SnapshotGroupSource // unimplemented group source methods
	// groups and err are configured together before discovery calls. ListActive
	// returns groups storage directly with err, so tests keep the slice and its
	// entries stable during reads.
	groups []SnapshotGroup // borrowed active group values
	err    error           // configured active-read failure cause
}

// schedulerSnapshotContextCacheStub returns the caller's context error from a
// snapshot read.
type schedulerSnapshotContextCacheStub struct {
	// Only GetSnapshot reports its supplied context error; other cache methods
	// remain unavailable through the nil embedding.
	SnapshotCache // unimplemented context cache methods
}

// schedulerSnapshotFallbackRepoStub counts platform source reads so
// cancellation tests can detect database fallback.
type schedulerSnapshotFallbackRepoStub struct {
	// Only platform and ungrouped single-platform readers count calls; other
	// source methods retain the nil embedding and cannot be used by this fixture.
	SnapshotProviderSource     // unimplemented fallback source methods
	calls                  int // serial fallback query count
}

// snapshotTestProvider holds provider identity and memberships exposed through
// cloned metadata slices.
type snapshotTestProvider struct {
	ID          int64  // fixture persisted provider ID
	Name        string // fixture provider display name
	Platform    string // fixture provider platform identity
	Status      string // fixture persisted provider status
	Schedulable bool   // fixture manual scheduling flag
	// GroupIDs stays shared across copies of snapshotTestProvider values;
	// SnapshotMetadata returns a cloned slice so metadata consumers can change
	// their membership slice independently.
	GroupIDs []int64 // fixture provider membership IDs
}

// retirementProviderSource filters borrowed fixtures by platform and
// optionally intercepts single-platform reads.
type retirementProviderSource struct {
	// Platform and grouped/ungrouped reader variants filter fixtures here;
	// identity readers remain unsupported through the nil embedded source.
	SnapshotProviderSource // unimplemented source method embedding
	// providers stays stable during default filtering. Matching input entries use
	// new result slice storage retaining the same objects; no matches leave a nil
	// result, while the hook owns its returned data.
	providers []SnapshotProvider // borrowed source provider objects
	// listPlatformFunc overrides only single-platform reads and their delegates,
	// receiving the caller context. Plural platform filtering bypasses this
	// callback even when it is configured.
	// single-platform query override
	listPlatformFunc func(context.Context, string) ([]SnapshotProvider, error)
}

// planSnapshotCache signals registry reads and can wait for cancellation at a
// shutdown barrier.
type planSnapshotCache struct {
	// Only ListBuckets supplies startup/shutdown observations; tests keep
	// discovery failing or stopped so other embedded cache methods are never
	// needed.
	SnapshotCache // unimplemented worker cache methods
	// calls is sent before the cancellable barrier. Tests provide a receiver or
	// buffered space because this notification send itself does not observe
	// cancellation.
	calls chan struct{} // registry read notification channel
	// block may be nil for immediate planned failure; a nonnil channel waits for
	// opening or caller cancellation after recording the read notification.
	block chan struct{} // registry read continuation barrier
}

// updateSnapshotValue supplies one provider identity for immediate
// cache-update forwarding assertions.
type updateSnapshotValue struct {
	id int64 /* fixture persisted provider ID */
}

// updateSnapshotCache records borrowed update arguments before returning its
// configured publication error.
type updateSnapshotCache struct {
	// SetProvider is the only supported operation; the cache-update tests avoid
	// all other methods supplied through this nil interface.
	SnapshotCache // unimplemented update cache methods
	// values records each SetProvider input before the configured err is
	// returned, so a failed call remains observable in the fixture.
	values []SnapshotProvider // borrowed provider update arguments
	err    error              // configured cache update error
}

// TestSchedulerRebuildBatchReusesSingleForcedQueryAndKeepsSnapshotsIndependent
// rebuilds the two reusable modes in two separate batches. It checks one query
// per batch, token capture before reads, and distinct per-bucket epochs with
// independent lock, write, and version observations.
func TestSchedulerRebuildBatchReusesSingleForcedQueryAndKeepsSnapshotsIndependent(t *testing.T) {
	const groupID int64 = 201
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newBatchSnapshotCache()
	repo := newBatchProviderQueryRepo()
	wantCaptures := 2
	// Observe the capture count when the source is entered, including the next
	// batch. Each round must prepare both bucket tokens before its shared query
	// begins.
	repo.beforeRun = func(batchProviderQueryKey) {
		require.Equal(t, wantCaptures, cache.captureCount(), "all tokens must be prepared before the first DB query")
		wantCaptures += 2
	}
	svc := newBatchQueryTestService(cache, repo)

	require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "first"))
	queryKey := batchProviderQueryKey{groupID: groupID, platform: PlatformOpenAI}
	require.Equal(t, 1, repo.callCount(queryKey))
	for _, bucket := range []SchedulerBucket{single, forced} {
		locks, attempts, version, writes := cache.bucketState(bucket)
		require.Equal(t, 1, locks, bucket.String())
		require.Equal(t, 1, attempts, bucket.String())
		require.Equal(t, 1, version, bucket.String())
		require.Len(t, writes, 1, bucket.String())
		require.Equal(t, "source", snapshotTestData(writes[0].providers[0]).Name, bucket.String())
		require.Equal(t, bucket, writes[0].token.Bucket)
	}
	_, _, _, singleWrites := cache.bucketState(single)
	_, _, _, forcedWrites := cache.bucketState(forced)
	require.NotEqual(t, singleWrites[0].token.Epoch, forcedWrites[0].token.Epoch)

	require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "second"))
	require.Equal(t, 2, repo.callCount(queryKey), "successful results must not be cached across rebuild batches")
	for _, bucket := range []SchedulerBucket{single, forced} {
		locks, attempts, version, writes := cache.bucketState(bucket)
		require.Equal(t, 2, locks, bucket.String())
		require.Equal(t, 2, attempts, bucket.String())
		require.Equal(t, 2, version, bucket.String())
		require.Len(t, writes, 2, bucket.String())
		require.Equal(t, "source", snapshotTestData(writes[1].providers[0]).Name, bucket.String())
	}
}

// TestSchedulerRebuildBatchReusesProviderPayloadForSingleForced runs two
// reusable bucket batches. It checks that each batch uses a full writer for
// single mode and an ID-only writer for forced mode, with a fresh source query
// in each batch.
func TestSchedulerRebuildBatchReusesProviderPayloadForSingleForced(t *testing.T) {
	const groupID int64 = 211
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newBatchSnapshotProviderIDCache()
	repo := newBatchProviderQueryRepo()
	svc := newBatchQueryTestService(cache, repo)

	for run := 1; run <= 2; run++ {
		require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "reuse"))
		full, idOnly := cache.reuseCounts(single)
		require.Equal(t, run, full)
		require.Zero(t, idOnly)
		full, idOnly = cache.reuseCounts(forced)
		require.Zero(t, full)
		require.Equal(t, run, idOnly)
	}
	require.Equal(t, 2, repo.callCount(batchProviderQueryKey{groupID: groupID, platform: PlatformOpenAI}), "提供商载荷不得跨重建批次复用")
}

// TestSchedulerRebuildBatchDoesNotReuseProviderPayloadAfterFirstWriterFailure
// injects the first full publication failure. The original error must return
// while the following forced bucket still uses ordinary SetSnapshot and
// records no ID-only reuse.
func TestSchedulerRebuildBatchDoesNotReuseProviderPayloadAfterFirstWriterFailure(t *testing.T) {
	const groupID int64 = 212
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	wantErr := errors.New("snapshot write failed")
	cache := newBatchSnapshotProviderIDCache()
	cache.setErrors[single] = wantErr
	svc := newBatchQueryTestService(cache, newBatchProviderQueryRepo())

	err := svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "failure")
	require.ErrorIs(t, err, wantErr)
	full, idOnly := cache.reuseCounts(single)
	require.Equal(t, 1, full)
	require.Zero(t, idOnly)
	full, idOnly = cache.reuseCounts(forced)
	require.Zero(t, full)
	require.Zero(t, idOnly)
	_, attempts, _, writes := cache.bucketState(forced)
	require.Equal(t, 1, attempts, "首次完整写失败后，后续桶必须走原 SetSnapshot")
	require.Len(t, writes, 1)
}

// TestSchedulerRebuildBatchDoesNotReuseProviderPayloadAfterLateFirstWriterFailure
// returns an error after the first base write succeeded. It checks that this
// incomplete full writer result cannot establish reusable IDs for the
// following forced bucket.
func TestSchedulerRebuildBatchDoesNotReuseProviderPayloadAfterLateFirstWriterFailure(t *testing.T) {
	const groupID int64 = 216
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	wantErr := errors.New("snapshot activation failed")
	cache := newBatchSnapshotProviderIDCache()
	cache.fullLateErr[single] = wantErr
	svc := newBatchQueryTestService(cache, newBatchProviderQueryRepo())

	err := svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "late-failure")
	require.ErrorIs(t, err, wantErr)
	full, idOnly := cache.reuseCounts(single)
	require.Equal(t, 1, full)
	require.Zero(t, idOnly)
	full, idOnly = cache.reuseCounts(forced)
	require.Zero(t, full)
	require.Zero(t, idOnly)
	_, attempts, _, writes := cache.bucketState(forced)
	require.Equal(t, 1, attempts, "首次激活失败后不得登记可复用 ID")
	require.Len(t, writes, 1)
}

// TestSchedulerRebuildBatchDoesNotReuseProviderPayloadAfterLockBusy skips the
// first bucket before encoding because its lock is busy. The following forced
// bucket must perform an ordinary full snapshot write with no reusable-writer
// calls.
func TestSchedulerRebuildBatchDoesNotReuseProviderPayloadAfterLockBusy(t *testing.T) {
	const groupID int64 = 213
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newBatchSnapshotProviderIDCache()
	cache.lockBusy[single] = true
	svc := newBatchQueryTestService(cache, newBatchProviderQueryRepo())

	require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "busy"))
	full, idOnly := cache.reuseCounts(single)
	require.Zero(t, full)
	require.Zero(t, idOnly)
	full, idOnly = cache.reuseCounts(forced)
	require.Zero(t, full)
	require.Zero(t, idOnly)
	_, attempts, _, writes := cache.bucketState(forced)
	require.Equal(t, 1, attempts)
	require.Len(t, writes, 1)
}

// TestSchedulerRebuildBatchKeepsMixedAndDifferentQueriesOnFullWrites mixes
// reusable OpenAI modes with another platform and mixed mode. Only the
// equivalent OpenAI pair uses ID reuse; the other buckets retain ordinary
// complete snapshot writes.
func TestSchedulerRebuildBatchKeepsMixedAndDifferentQueriesOnFullWrites(t *testing.T) {
	const groupID int64 = 214
	openAISingle := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	openAIForced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	anthropicSingle := SchedulerBucket{GroupID: groupID, Platform: PlatformAnthropic, Mode: SchedulerModeSingle}
	anthropicMixed := SchedulerBucket{GroupID: groupID, Platform: PlatformAnthropic, Mode: SchedulerModeMixed}
	cache := newBatchSnapshotProviderIDCache()
	svc := newBatchQueryTestService(cache, newBatchProviderQueryRepo())

	require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{openAISingle, openAIForced, anthropicSingle, anthropicMixed}, "scope"))
	full, idOnly := cache.reuseCounts(openAISingle)
	require.Equal(t, 1, full)
	require.Zero(t, idOnly)
	full, idOnly = cache.reuseCounts(openAIForced)
	require.Zero(t, full)
	require.Equal(t, 1, idOnly)
	full, idOnly = cache.reuseCounts(anthropicSingle)
	require.Zero(t, full)
	require.Zero(t, idOnly)
	_, attempts, _, writes := cache.bucketState(anthropicSingle)
	require.Equal(t, 1, attempts)
	require.Len(t, writes, 1)
	full, idOnly = cache.reuseCounts(anthropicMixed)
	require.Zero(t, full)
	require.Zero(t, idOnly)
	_, attempts, _, _ = cache.bucketState(anthropicMixed)
	require.Equal(t, 1, attempts, "mixed 桶必须继续走原 SetSnapshot")
}

// TestSchedulerRebuildBatchPropagatesProviderIDOnlyWriteFailure injects an
// error into the second bucket's ID-only publication. It checks the returned
// cause and one ID-only call, with no silent full-writer retry.
func TestSchedulerRebuildBatchPropagatesProviderIDOnlyWriteFailure(t *testing.T) {
	const groupID int64 = 215
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	wantErr := errors.New("id-only write failed")
	cache := newBatchSnapshotProviderIDCache()
	cache.idOnlyError[forced] = wantErr
	svc := newBatchQueryTestService(cache, newBatchProviderQueryRepo())

	err := svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "id-error")
	require.ErrorIs(t, err, wantErr)
	full, idOnly := cache.reuseCounts(forced)
	require.Zero(t, full, "ID-only 失败不得静默回退为完整写")
	require.Equal(t, 1, idOnly)
}

// TestSchedulerRebuildBatchReusesSuccessfulEmptyProviderIDs supplies a
// successful empty encoding result from the first writer. It checks that map
// presence still sends the forced bucket through ID-only publication and
// records an empty provider argument slice.
func TestSchedulerRebuildBatchReusesSuccessfulEmptyProviderIDs(t *testing.T) {
	const groupID int64 = 217
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newBatchSnapshotProviderIDCache()
	cache.returnEmpty = true
	svc := newBatchQueryTestService(cache, newBatchProviderQueryRepo())

	require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "empty"))
	full, idOnly := cache.reuseCounts(single)
	require.Equal(t, 1, full)
	require.Zero(t, idOnly)
	full, idOnly = cache.reuseCounts(forced)
	require.Zero(t, full)
	require.Equal(t, 1, idOnly, "已成功缓存的空 ID 集也必须通过 map presence 复用")
	_, _, _, writes := cache.bucketState(forced)
	require.Len(t, writes, 1)
	require.Empty(t, writes[0].providers)
}

// TestSchedulerRebuildBatchReusesProviderPayloadForSimpleGroupZero rebuilds
// single and forced buckets for group zero. Successful completion must still
// use one full encoding path followed by the ID-only path for that equivalent
// query key.
func TestSchedulerRebuildBatchReusesProviderPayloadForSimpleGroupZero(t *testing.T) {
	single := SchedulerBucket{GroupID: 0, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: 0, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newBatchSnapshotProviderIDCache()
	svc := newBatchQueryTestService(cache, newBatchProviderQueryRepo())

	require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "test"))
	full, idOnly := cache.reuseCounts(single)
	require.Equal(t, 1, full)
	require.Zero(t, idOnly)
	full, idOnly = cache.reuseCounts(forced)
	require.Zero(t, full)
	require.Equal(t, 1, idOnly)
}

// TestSchedulerProviderQueryCacheReleasesSnapshotProviderIDs manually retains
// encoded IDs for two consumers. The first release keeps those IDs, while the
// final release removes them and leaves all query-cache maps empty.
func TestSchedulerProviderQueryCacheReleasesSnapshotProviderIDs(t *testing.T) {
	single := schedulerBucketWriteTask{bucket: SchedulerBucket{GroupID: 218, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}}
	forced := schedulerBucketWriteTask{bucket: SchedulerBucket{GroupID: 218, Platform: PlatformOpenAI, Mode: SchedulerModeForced}}
	queries := newSchedulerProviderQueryCache([]schedulerBucketWriteTask{single, forced})
	key, ok := schedulerProviderQueryKeyForBucket(single.bucket)
	require.True(t, ok)
	queries.snapshotProviderIDs[key] = []int64{1, 2}

	queries.release(single.bucket)
	require.Contains(t, queries.snapshotProviderIDs, key)
	queries.release(forced.bucket)
	require.NotContains(t, queries.snapshotProviderIDs, key)
	require.Empty(t, queries.remaining)
	require.Empty(t, queries.providers)
}

// TestSchedulerRebuildBatchKeepsMixedAndDifferentKeysIndependent combines
// modes, platforms, and positive/nonpositive group IDs. It checks separate
// positive queries, uncached mixed-mode reads through the ordinary platform
// source, and no source reads for nonpositive groups while each bucket records
// publication.
func TestSchedulerRebuildBatchKeepsMixedAndDifferentKeysIndependent(t *testing.T) {
	const groupID int64 = 202
	buckets := []SchedulerBucket{
		{GroupID: groupID, Platform: PlatformAnthropic, Mode: SchedulerModeSingle},
		{GroupID: groupID, Platform: PlatformAnthropic, Mode: SchedulerModeForced},
		{GroupID: groupID, Platform: PlatformAnthropic, Mode: SchedulerModeMixed},
		{GroupID: groupID + 1, Platform: PlatformAnthropic, Mode: SchedulerModeSingle},
		{GroupID: groupID, Platform: PlatformGemini, Mode: SchedulerModeForced},
		{GroupID: 0, Platform: PlatformOpenAI, Mode: SchedulerModeSingle},
		{GroupID: -1, Platform: PlatformOpenAI, Mode: SchedulerModeForced},
	}
	cache := newBatchSnapshotCache()
	repo := newBatchProviderQueryRepo()
	svc := newBatchQueryTestService(cache, repo)

	require.NoError(t, svc.rebuildBuckets(context.Background(), buckets, "test"))
	require.Equal(t, 2, repo.callCount(batchProviderQueryKey{groupID: groupID, platform: PlatformAnthropic}))
	require.Zero(t, repo.callCount(batchProviderQueryKey{groupID: groupID, platform: PlatformAnthropic, mixed: true}))
	require.Equal(t, 1, repo.callCount(batchProviderQueryKey{groupID: groupID + 1, platform: PlatformAnthropic}))
	require.Equal(t, 1, repo.callCount(batchProviderQueryKey{groupID: groupID, platform: PlatformGemini}))
	require.Zero(t, repo.callCount(batchProviderQueryKey{platform: PlatformOpenAI}), "没有有效分组时不查询提供商")
	for _, bucket := range buckets {
		locks, attempts, version, _ := cache.bucketState(bucket)
		require.Equal(t, 1, locks, bucket.String())
		require.Equal(t, 1, attempts, bucket.String())
		require.Equal(t, 1, version, bucket.String())
	}
}

// TestSchedulerRebuildBatchKeepsBucketGroupsIndependent places a positive
// group's single bucket beside a group-zero forced bucket. It checks that the
// positive query executes once while group zero causes no ungrouped source
// read.
func TestSchedulerRebuildBatchKeepsBucketGroupsIndependent(t *testing.T) {
	single := SchedulerBucket{GroupID: 204, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: 0, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newBatchSnapshotCache()
	repo := newBatchProviderQueryRepo()
	svc := newBatchQueryTestService(cache, repo)

	require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "test"))
	require.Equal(t, 1, repo.callCount(batchProviderQueryKey{groupID: 204, platform: PlatformOpenAI}))
	require.Zero(t, repo.callCount(batchProviderQueryKey{platform: PlatformOpenAI}))
}

// TestSchedulerRebuildBatchDoesNotCacheMixedOrHistoricalQueries repeats a
// mixed or unknown-mode task under one captured token. It checks two
// independent source reads and publications with no retained provider-query
// cache entry.
func TestSchedulerRebuildBatchDoesNotCacheMixedOrHistoricalQueries(t *testing.T) {
	for _, tc := range []struct {
		name   string                // uncacheable mode case label
		bucket SchedulerBucket       // repeated uncacheable bucket identity
		key    batchProviderQueryKey // expected source query key
	}{
		{
			name:   "mixed",
			bucket: SchedulerBucket{GroupID: 204, Platform: PlatformAnthropic, Mode: SchedulerModeMixed},
			key:    batchProviderQueryKey{groupID: 204, platform: PlatformAnthropic},
		},
		{
			name:   "historical",
			bucket: SchedulerBucket{GroupID: 204, Platform: PlatformOpenAI, Mode: "unknown"},
			key:    batchProviderQueryKey{groupID: 204, platform: PlatformOpenAI},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := newBatchSnapshotCache()
			token, err := cache.CaptureBucketWriteToken(context.Background(), tc.bucket)
			require.NoError(t, err)
			repo := newBatchProviderQueryRepo()
			svc := newBatchQueryTestService(cache, repo)
			tasks := []schedulerBucketWriteTask{
				{bucket: tc.bucket, token: token},
				{bucket: tc.bucket, token: token},
			}
			queries := newSchedulerProviderQueryCache(tasks)

			require.NoError(t, svc.rebuildPreparedBucketTasks(context.Background(), tasks, "test", false, queries))
			require.Equal(t, 2, repo.callCount(tc.key))
			require.Empty(t, queries.providers)
			locks, attempts, version, _ := cache.bucketState(tc.bucket)
			require.Equal(t, 2, locks)
			require.Equal(t, 2, attempts)
			require.Equal(t, 2, version)
		})
	}
}

// TestSchedulerRebuildBatchRetriesQueryFailureForFollowingBucket scripts a
// failed source read followed by a valid result. The first cause remains
// returned, and the equivalent forced bucket must retry the query and publish
// the later provider.
func TestSchedulerRebuildBatchRetriesQueryFailureForFollowingBucket(t *testing.T) {
	const groupID int64 = 205
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	wantErr := errors.New("first query failed")
	key := batchProviderQueryKey{groupID: groupID, platform: PlatformOpenAI}
	repo := newBatchProviderQueryRepo()
	repo.results[key] = []batchProviderQueryResult{
		{err: wantErr},
		{providers: []SnapshotProvider{snapshotTestProvider{ID: 2051, Name: "retry", Platform: PlatformOpenAI}}},
	}
	cache := newBatchSnapshotCache()
	svc := newBatchQueryTestService(cache, repo)

	err := svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "test")
	require.ErrorIs(t, err, wantErr)
	require.Equal(t, 2, repo.callCount(key), "failed queries must not enter the batch cache")
	_, singleAttempts, singleVersion, _ := cache.bucketState(single)
	_, forcedAttempts, forcedVersion, forcedWrites := cache.bucketState(forced)
	require.Zero(t, singleAttempts)
	require.Zero(t, singleVersion)
	require.Equal(t, 1, forcedAttempts)
	require.Equal(t, 1, forcedVersion)
	require.Equal(t, "retry", snapshotTestData(forcedWrites[0].providers[0]).Name)
}

// TestSchedulerFullRebuildSharesSuccessfulQueryAcrossStrictAndOrdinarySegments
// fences the strict publication after its source read succeeded. It checks
// that ordinary work still publishes from the same successful query while the
// strict error remains returned.
func TestSchedulerFullRebuildSharesSuccessfulQueryAcrossStrictAndOrdinarySegments(t *testing.T) {
	const groupID int64 = 206
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newBatchSnapshotCache()
	cache.setErrors[single] = ErrSchedulerBucketWriteFenced
	singleToken, err := cache.CaptureBucketWriteToken(context.Background(), single)
	require.NoError(t, err)
	forcedToken, err := cache.CaptureBucketWriteToken(context.Background(), forced)
	require.NoError(t, err)
	repo := newBatchProviderQueryRepo()
	svc := newBatchQueryTestService(cache, repo)

	err = svc.prepareAndRebuildFullSnapshot(
		context.Background(),
		[]schedulerBucketWriteTask{{bucket: forced, token: forcedToken}},
		[]schedulerBucketWriteTask{{bucket: single, token: singleToken}},
		nil,
		"test",
	)
	require.ErrorIs(t, err, ErrSchedulerBucketWriteFenced)
	require.Equal(t, 1, repo.callCount(batchProviderQueryKey{groupID: groupID, platform: PlatformOpenAI}), "SetSnapshot failure must not discard a successful query")
	_, singleAttempts, singleVersion, _ := cache.bucketState(single)
	_, forcedAttempts, forcedVersion, _ := cache.bucketState(forced)
	require.Equal(t, 1, singleAttempts)
	require.Zero(t, singleVersion)
	require.Equal(t, 1, forcedAttempts)
	require.Equal(t, 1, forcedVersion)
}

// TestSchedulerRebuildBatchPreservesLockBusyAndFencingPolicy injects ordinary
// contention, strict contention, and ordinary fencing in separate cases. It
// checks each branch's error policy and confirms that the following ordinary
// bucket continues with one eligible source query.
func TestSchedulerRebuildBatchPreservesLockBusyAndFencingPolicy(t *testing.T) {
	const groupID int64 = 207
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}

	t.Run("ordinary lock busy skips only that bucket", func(t *testing.T) {
		cache := newBatchSnapshotCache()
		cache.lockBusy[single] = true
		repo := newBatchProviderQueryRepo()
		svc := newBatchQueryTestService(cache, repo)

		require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "test"))
		require.Equal(t, 1, repo.callCount(batchProviderQueryKey{groupID: groupID, platform: PlatformOpenAI}))
		_, singleAttempts, _, _ := cache.bucketState(single)
		_, forcedAttempts, forcedVersion, _ := cache.bucketState(forced)
		require.Zero(t, singleAttempts)
		require.Equal(t, 1, forcedAttempts)
		require.Equal(t, 1, forcedVersion)
	})

	t.Run("strict lock busy is returned while ordinary work continues", func(t *testing.T) {
		cache := newBatchSnapshotCache()
		cache.lockBusy[single] = true
		singleToken, err := cache.CaptureBucketWriteToken(context.Background(), single)
		require.NoError(t, err)
		forcedToken, err := cache.CaptureBucketWriteToken(context.Background(), forced)
		require.NoError(t, err)
		repo := newBatchProviderQueryRepo()
		svc := newBatchQueryTestService(cache, repo)

		err = svc.prepareAndRebuildFullSnapshot(
			context.Background(),
			[]schedulerBucketWriteTask{{bucket: forced, token: forcedToken}},
			[]schedulerBucketWriteTask{{bucket: single, token: singleToken}},
			nil,
			"test",
		)
		require.ErrorIs(t, err, ErrSchedulerBucketRebuildBusy)
		require.Equal(t, 1, repo.callCount(batchProviderQueryKey{groupID: groupID, platform: PlatformOpenAI}))
		_, forcedAttempts, forcedVersion, _ := cache.bucketState(forced)
		require.Equal(t, 1, forcedAttempts)
		require.Equal(t, 1, forcedVersion)
	})

	t.Run("ordinary fencing stays non-fatal", func(t *testing.T) {
		cache := newBatchSnapshotCache()
		cache.setErrors[single] = ErrSchedulerBucketWriteFenced
		repo := newBatchProviderQueryRepo()
		svc := newBatchQueryTestService(cache, repo)

		require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "test"))
		require.Equal(t, 1, repo.callCount(batchProviderQueryKey{groupID: groupID, platform: PlatformOpenAI}))
		_, singleAttempts, singleVersion, _ := cache.bucketState(single)
		_, forcedAttempts, forcedVersion, _ := cache.bucketState(forced)
		require.Equal(t, 1, singleAttempts)
		require.Zero(t, singleVersion)
		require.Equal(t, 1, forcedAttempts)
		require.Equal(t, 1, forcedVersion)
	})
}

// TestSchedulerRebuildBatchReleasesResultsAfterLastConsumer processes 128
// adjacent reusable pairs, including busy and failed locks. It checks at most
// one resident provider result, one source read per pair, empty cache maps
// afterward, and the retained lock error.
func TestSchedulerRebuildBatchReleasesResultsAfterLastConsumer(t *testing.T) {
	// Many adjacent pairs expose retained results from earlier groups. The first
	// two pairs also end through busy/error paths, so consumer cleanup must
	// include those exits.
	const groups = 128
	cache := newBatchSnapshotCache()
	repo := newBatchProviderQueryRepo()
	tasks := make([]schedulerBucketWriteTask, 0, groups*2)
	wantLockErr := errors.New("lock failed")
	for i := 1; i <= groups; i++ {
		groupID := int64(300 + i)
		single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
		forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
		if i == 1 {
			cache.lockBusy[single] = true
		}
		if i == 2 {
			cache.lockErrors[single] = wantLockErr
		}
		for _, bucket := range []SchedulerBucket{single, forced} {
			token, err := cache.CaptureBucketWriteToken(context.Background(), bucket)
			require.NoError(t, err)
			tasks = append(tasks, schedulerBucketWriteTask{bucket: bucket, token: token})
		}
	}
	queries := newSchedulerProviderQueryCache(tasks)
	maxResident := 0
	// Sample resident query entries at publication boundaries. Adjacent
	// single/forced pairs let the next write expose whether the previous pair's
	// final consumer released its retained result.
	cache.beforeSet = func() {
		if resident := len(queries.providers); resident > maxResident {
			maxResident = resident
		}
	}
	svc := newBatchQueryTestService(cache, repo)

	err := svc.rebuildPreparedBucketTasks(context.Background(), tasks, "test", false, queries)
	require.ErrorIs(t, err, wantLockErr)
	require.LessOrEqual(t, maxResident, 1, "adjacent single/forced pairs must not accumulate full-batch results")
	require.Empty(t, queries.providers)
	require.Empty(t, queries.remaining)
	for i := 1; i <= groups; i++ {
		key := batchProviderQueryKey{groupID: int64(300 + i), platform: PlatformOpenAI}
		require.Equal(t, 1, repo.callCount(key), key)
	}
}

// BenchmarkSchedulerRebuildBatchQueryReuse measures local two-bucket
// rebuilding with one or ten thousand synthetic slice entries. It reports
// allocation/timing work through count-only fixture writers; the entries are
// nil and no Redis encoding or network latency is measured.
func BenchmarkSchedulerRebuildBatchQueryReuse(b *testing.B) {
	const groupID int64 = 208
	buckets := []SchedulerBucket{
		{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle},
		{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced},
	}
	for _, tc := range []struct {
		name string // benchmark workload display label
		size int    // synthetic provider slice length
	}{
		{name: "1_provider", size: 1},
		{name: "10000_providers", size: 10_000},
	} {
		b.Run(tc.name, func(b *testing.B) {
			// The writer consumes slice length, so these entries intentionally stay
			// nil. Allocate workload input before timing to isolate rebuilding and
			// query-cache/lease work.
			providers := make([]SnapshotProvider, tc.size)
			svc := newBatchQueryTestService(
				&batchQueryBenchmarkCache{},
				&batchQueryBenchmarkRepo{providers: providers},
			)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := svc.rebuildBuckets(context.Background(), buckets, "benchmark"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestSchedulerBulkProviderEventScopesOpenAIRebuildToFreshPlatform moves an
// OpenAI provider between recorded memberships. It checks current and former
// groups' OpenAI/shared buckets, one immediate provider update, and no
// deletion.
func TestSchedulerBulkProviderEventScopesOpenAIRebuildToFreshPlatform(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := newBulkEventProviderRepo(&snapshotTestProvider{ID: 1, Platform: PlatformOpenAI, GroupIDs: []int64{12}})
	svc := newBulkEventTestService(cache, repo)

	err := svc.handleBulkProviderEvent(context.Background(), bulkEventPayload([]int64{1}, []int64{11}), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	require.ElementsMatch(t, schedulerBucketsForTest([]int64{11, 12}, PlatformOpenAI), cache.capturedBuckets())
	set, deleted := cache.providerWrites()
	require.Equal(t, []int64{1}, set)
	require.Empty(t, deleted)
}

// TestSchedulerBulkProviderEventScopesQoderRebuildToFreshPlatform supplies a
// Qoder provider with current and former groups. It checks that Qoder and
// shared single/forced buckets are captured for both memberships.
func TestSchedulerBulkProviderEventScopesQoderRebuildToFreshPlatform(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := newBulkEventProviderRepo(&snapshotTestProvider{ID: 13, Platform: PlatformQoder, GroupIDs: []int64{82}})
	svc := newBulkEventTestService(cache, repo)

	err := svc.handleBulkProviderEvent(context.Background(), bulkEventPayload([]int64{13}, []int64{81}), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	require.ElementsMatch(t, schedulerBucketsForTest([]int64{81, 82}, PlatformQoder), cache.capturedBuckets())
}

// TestSchedulerBulkProviderEventRebuildsOpenAIUngroupedBucket supplies an
// OpenAI provider with no group memberships. It checks normalization to
// group-zero OpenAI/shared single and forced buckets rather than a positive
// group query.
func TestSchedulerBulkProviderEventRebuildsOpenAIUngroupedBucket(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := newBulkEventProviderRepo(&snapshotTestProvider{ID: 6, Platform: PlatformOpenAI})
	svc := newBulkEventTestService(cache, repo)

	err := svc.handleBulkProviderEvent(context.Background(), bulkEventPayload([]int64{6}, nil), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	require.ElementsMatch(t, schedulerBucketsForTest([]int64{0}, PlatformOpenAI), cache.capturedBuckets())
}

// TestSchedulerBulkProviderEventKeepsGroupedAndUngroupedBuckets combines
// grouped and ungrouped OpenAI fixtures in one event. It checks that both the
// positive membership and canonical group-zero OpenAI/shared buckets remain in
// the captured rebuild set.
func TestSchedulerBulkProviderEventKeepsGroupedAndUngroupedBuckets(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := newBulkEventProviderRepo(
		&snapshotTestProvider{ID: 7, Platform: PlatformOpenAI, GroupIDs: []int64{51}},
		&snapshotTestProvider{ID: 8, Platform: PlatformOpenAI},
	)
	svc := newBulkEventTestService(cache, repo)

	err := svc.handleBulkProviderEvent(context.Background(), bulkEventPayload([]int64{7, 8}, nil), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	require.ElementsMatch(t, schedulerBucketsForTest([]int64{0, 51}, PlatformOpenAI), cache.capturedBuckets())
}

// TestSchedulerBulkProviderEventDoesNotCrossCurrentGroupsBetweenPlatforms
// combines OpenAI and Grok providers with different current memberships and
// one former group. It checks platform-specific current groups and common
// former-group rebuilding without crossing current memberships.
func TestSchedulerBulkProviderEventDoesNotCrossCurrentGroupsBetweenPlatforms(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := newBulkEventProviderRepo(
		&snapshotTestProvider{ID: 9, Platform: PlatformOpenAI, GroupIDs: []int64{61}},
		&snapshotTestProvider{ID: 10, Platform: PlatformGrok, GroupIDs: []int64{62}},
	)
	svc := newBulkEventTestService(cache, repo)

	err := svc.handleBulkProviderEvent(context.Background(), bulkEventPayload([]int64{9, 10}, []int64{63}), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	want := append(
		schedulerBucketsForTest([]int64{61, 63}, PlatformOpenAI),
		schedulerBucketsForTest([]int64{62, 63}, PlatformGrok)...,
	)
	require.ElementsMatch(t, dedupeBuckets(want), cache.capturedBuckets())
}

// TestSchedulerBulkProviderEventKeepsGroupMembershipIn supplies different
// payload and current provider memberships. It checks that direct
// SnapshotService construction retains both groups' OpenAI/shared buckets in
// the bulk rebuild set.
func TestSchedulerBulkProviderEventKeepsGroupMembershipIn(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := newBulkEventProviderRepo(&snapshotTestProvider{ID: 11, Platform: PlatformOpenAI, GroupIDs: []int64{71}})
	svc := NewSnapshotService(cache, nil, repo, nil, &SnapshotOptions{})

	err := svc.handleBulkProviderEvent(context.Background(), bulkEventPayload([]int64{11}, []int64{72}), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	require.ElementsMatch(t, schedulerBucketsForTest([]int64{71, 72}, PlatformOpenAI), cache.capturedBuckets())
}

// TestSchedulerBulkProviderEventRefreshesAntigravityAndSharedPool supplies an
// Antigravity provider with current and former groups. It checks that both
// Antigravity and platform-empty shared buckets are included for each affected
// group.
func TestSchedulerBulkProviderEventRefreshesAntigravityAndSharedPool(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := newBulkEventProviderRepo(&snapshotTestProvider{ID: 2, Platform: PlatformAntigravity, GroupIDs: []int64{22}})
	svc := newBulkEventTestService(cache, repo)

	err := svc.handleBulkProviderEvent(context.Background(), bulkEventPayload([]int64{2}, []int64{21}), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	require.ElementsMatch(t,
		schedulerBucketsForTest([]int64{21, 22}, PlatformAntigravity),
		cache.capturedBuckets(),
	)
}

// TestSchedulerBulkProviderEventMissingProviderFallsBackToAllPlatforms
// requests a provider absent from the returned source records. It checks
// deletion of that missing ID, update of the existing one, and all canonical
// platforms for former/current groups.
func TestSchedulerBulkProviderEventMissingProviderFallsBackToAllPlatforms(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := newBulkEventProviderRepo(&snapshotTestProvider{ID: 3, Platform: PlatformOpenAI, GroupIDs: []int64{32}})
	svc := newBulkEventTestService(cache, repo)

	err := svc.handleBulkProviderEvent(context.Background(), bulkEventPayload([]int64{3, 4}, []int64{31}), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	platforms := schedulerSnapshotPlatforms()
	require.ElementsMatch(t, schedulerBucketsForTest([]int64{31, 32}, platforms...), cache.capturedBuckets())
	set, deleted := cache.providerWrites()
	require.Equal(t, []int64{3}, set)
	require.Equal(t, []int64{4}, deleted)
}

// TestSchedulerBulkProviderEventUnknownPlatformFallsBackToAllPlatforms
// supplies a provider with no recognized platform identity. It checks
// successful broad rebuilding of every canonical/shared platform for both
// former and current groups.
func TestSchedulerBulkProviderEventUnknownPlatformFallsBackToAllPlatforms(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := newBulkEventProviderRepo(&snapshotTestProvider{ID: 5, GroupIDs: []int64{42}})
	svc := newBulkEventTestService(cache, repo)

	err := svc.handleBulkProviderEvent(context.Background(), bulkEventPayload([]int64{5}, []int64{41}), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	platforms := schedulerSnapshotPlatforms()
	require.ElementsMatch(t, schedulerBucketsForTest([]int64{41, 42}, platforms...), cache.capturedBuckets())
}

// TestSchedulerFullRebuildActiveTombstoneDoesNotBlockFollowingGroupEvent polls
// a full rebuild before a group-change event for an active tombstoned group.
// It checks fresh authority and leased reopening for both events, two query
// sets, and watermark advancement only after both finish.
func TestSchedulerFullRebuildActiveTombstoneDoesNotBlockFollowingGroupEvent(t *testing.T) {
	const groupID int64 = 101
	canonical := schedulerBucketsForGroup(groupID)
	cache := newFullRebuildLifecycleCache()
	require.NoError(t, cache.retirementRaceCache.RetireBucket(context.Background(), canonical[0]))
	groups := &fullRebuildLifecycleGroupRepo{
		activeIDs: []int64{groupID},
		fresh: map[int64]*SnapshotGroup{
			groupID: {ID: groupID, Status: StatusActive, Hydrated: true},
		},
		freshErr: make(map[int64]error),
	}
	providers := &fullRebuildProviderRepo{}
	outbox := &outboxCleanupRepo{events: []SchedulerOutboxEvent{
		{ID: 1, EventType: SchedulerOutboxEventFullRebuild},
		{ID: 2, EventType: SchedulerOutboxEventGroupChanged, GroupID: ptrInt64(groupID)},
	}}
	svc := newFullRebuildLifecycleService(cache, outbox, providers, groups)

	svc.pollOutbox()

	require.Equal(t, int64(2), cache.currentWatermark())
	cache.mu.Lock()
	require.Equal(t, []int64{2}, cache.watermarkWrites)
	cache.mu.Unlock()
	activeCalls, fallbackCalls, freshCalls := groups.stats()
	require.Equal(t, 1, activeCalls)
	require.Zero(t, fallbackCalls)
	require.Equal(t, []int64{groupID, groupID}, freshCalls)
	require.Len(t, cache.tokens(), len(canonical)*2, "full rebuild and the following group event must each run fresh authority")
	_, reopenHeld := cache.lifecycleMutationLeaseStates()
	require.Len(t, reopenHeld, len(canonical)*2)
	for _, held := range reopenHeld {
		require.True(t, held)
	}
	require.Equal(t, expectedCanonicalProviderQueryCount()*2, providers.callCount())
}

// TestSchedulerFullRebuildGlobalReadErrorsFailBeforeMutationOrDB injects
// registry, active-ID, and fallback discovery failures separately. It checks
// error provenance, reader selection, and zero token capture, lifecycle
// mutations, publication attempts, or provider reads after failed global
// discovery.
func TestSchedulerFullRebuildGlobalReadErrorsFailBeforeMutationOrDB(t *testing.T) {
	t.Run("list buckets", func(t *testing.T) {
		cache := newFullRebuildLifecycleCache()
		cache.listErr = errors.New("registry failed")
		groups := &fullRebuildLifecycleGroupRepo{fresh: make(map[int64]*SnapshotGroup), freshErr: make(map[int64]error)}
		providers := &fullRebuildProviderRepo{}
		svc := newFullRebuildLifecycleService(cache, nil, providers, groups)

		err := svc.rebuildFullSnapshot(context.Background(), "test")
		require.ErrorIs(t, err, cache.listErr)
		activeCalls, fallbackCalls, freshCalls := groups.stats()
		require.Zero(t, activeCalls)
		require.Zero(t, fallbackCalls)
		require.Empty(t, freshCalls)
		requireFullRebuildNoMutationOrDB(t, cache, providers)
	})

	t.Run("list active ids without fallback", func(t *testing.T) {
		cache := newFullRebuildLifecycleCache()
		groups := &fullRebuildLifecycleGroupRepo{
			activeIDsErr:  errors.New("active ids failed"),
			listActiveErr: errors.New("fallback must not run"),
			fresh:         make(map[int64]*SnapshotGroup),
			freshErr:      make(map[int64]error),
		}
		providers := &fullRebuildProviderRepo{}
		svc := newFullRebuildLifecycleService(cache, nil, providers, groups)

		err := svc.rebuildFullSnapshot(context.Background(), "test")
		require.ErrorIs(t, err, groups.activeIDsErr)
		activeCalls, fallbackCalls, freshCalls := groups.stats()
		require.Equal(t, 1, activeCalls)
		require.Zero(t, fallbackCalls)
		require.Empty(t, freshCalls)
		requireFullRebuildNoMutationOrDB(t, cache, providers)
	})

	t.Run("list active fallback", func(t *testing.T) {
		cache := newFullRebuildLifecycleCache()
		groups := &fullRebuildFallbackGroupRepo{err: errors.New("active groups failed")}
		providers := &fullRebuildProviderRepo{}
		svc := newFullRebuildLifecycleService(cache, nil, providers, groups)

		err := svc.rebuildFullSnapshot(context.Background(), "test")
		require.ErrorIs(t, err, groups.err)
		groups.mu.Lock()
		require.Equal(t, 1, groups.listCalls)
		groups.mu.Unlock()
		requireFullRebuildNoMutationOrDB(t, cache, providers)
	})
}

// TestSchedulerFullRebuildFreshActivePreparesEveryTokenBeforeFirstDB discovers
// an apparently stale historical group whose fresh authority is active. Its
// first-read hook checks completed canonical reopening, released lifecycle
// ownership, and all ordinary tokens before provider reads or historical
// publication.
func TestSchedulerFullRebuildFreshActivePreparesEveryTokenBeforeFirstDB(t *testing.T) {
	const groupID int64 = 102
	historical := SchedulerBucket{GroupID: groupID, Platform: "legacy", Mode: "unknown"}
	cache := newFullRebuildLifecycleCache(historical)
	groups := &fullRebuildLifecycleGroupRepo{
		fresh: map[int64]*SnapshotGroup{
			groupID: {ID: groupID, Status: StatusActive, Hydrated: true},
		},
		freshErr: make(map[int64]error),
	}
	providers := &fullRebuildProviderRepo{}
	var capturesAtFirstDB int
	// Inspect the first provider-read boundary while discovery observations are
	// stable. Completed reopen tokens and released owner state must precede this
	// hook's query result.
	providers.beforeFirst = func() {
		capturesAtFirstDB = cache.captureAttemptCount()
		held, reopenCount := cache.leaseHeldAndTokenCount()
		require.False(t, held)
		require.Equal(t, len(schedulerBucketsForGroup(groupID)), reopenCount)
		require.Equal(t, len(schedulerCanonicalBuckets(0))+1, capturesAtFirstDB, "C(0) and the historical bucket must be captured before DB")
	}
	svc := newFullRebuildLifecycleService(cache, nil, providers, groups)

	require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "test"))
	require.Equal(t, capturesAtFirstDB, cache.captureAttemptCount())
	require.Equal(t, expectedCanonicalProviderQueryCount()+1, providers.callCount())
	require.Equal(t, expectedCanonicalProviderQueryCount()+1, providers.groupCallCount(groupID))
	_, historicalPublished := cache.counts(historical)
	require.Equal(t, 1, historicalPublished)
	activeCalls, fallbackCalls, freshCalls := groups.stats()
	require.Equal(t, 1, activeCalls)
	require.Zero(t, fallbackCalls)
	require.Equal(t, []int64{groupID}, freshCalls)
	_, _, listCalls := cache.lifecycleCounts()
	require.Equal(t, 1, listCalls, "known Rg must prevent a second registry read")
}

// TestSchedulerFullRebuildOrdinaryCaptureErrorReturnsBeforeFirstDB injects a
// historical group-zero token capture error. It checks all canonical/ordinary
// captures were attempted and the error returned before any provider query or
// publication attempt.
func TestSchedulerFullRebuildOrdinaryCaptureErrorReturnsBeforeFirstDB(t *testing.T) {
	first := SchedulerBucket{GroupID: 0, Platform: "legacy-a", Mode: "unknown"}
	last := SchedulerBucket{GroupID: 0, Platform: "legacy-b", Mode: "unknown"}
	cache := newFullRebuildLifecycleCache(first, last)
	wantErr := errors.New("capture failed")
	cache.captureErrors[last.String()] = wantErr
	groups := &fullRebuildLifecycleGroupRepo{fresh: make(map[int64]*SnapshotGroup), freshErr: make(map[int64]error)}
	providers := &fullRebuildProviderRepo{}
	svc := newFullRebuildLifecycleService(cache, nil, providers, groups)

	err := svc.rebuildFullSnapshot(context.Background(), "test")
	require.ErrorIs(t, err, wantErr)
	require.Equal(t, len(schedulerCanonicalBuckets(0))+2, cache.captureAttemptCount(), "all canonical and ordinary captures must be attempted before returning")
	require.Zero(t, providers.callCount())
	require.Zero(t, cache.totalSetAttempts())
}

// TestSchedulerFullRebuildPreservesGroupZeroActiveHistoricalAndInvalidRegistryBuckets
// combines active canonical, mixed, and historical identities with zero and
// negative group IDs. It checks their publication counts and expected active
// queries without lifecycle retirement or reopen tokens.
func TestSchedulerFullRebuildPreservesGroupZeroActiveHistoricalAndInvalidRegistryBuckets(t *testing.T) {
	const groupID int64 = 103
	groupZeroHistorical := SchedulerBucket{GroupID: 0, Platform: "legacy-zero", Mode: "unknown"}
	activeHistorical := SchedulerBucket{GroupID: groupID, Platform: "legacy-active", Mode: "unknown"}
	activeForced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	activeMixed := SchedulerBucket{GroupID: groupID, Platform: PlatformAnthropic, Mode: SchedulerModeMixed}
	invalidHistorical := SchedulerBucket{GroupID: -7, Platform: "legacy-invalid", Mode: "unknown"}
	cache := newFullRebuildLifecycleCache(groupZeroHistorical, activeHistorical, activeForced, activeMixed, invalidHistorical)
	groups := &fullRebuildFallbackGroupRepo{groups: []SnapshotGroup{{ID: groupID, Status: StatusActive}}}
	providers := &fullRebuildProviderRepo{}
	svc := newFullRebuildLifecycleService(cache, nil, providers, groups)

	require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "test"))
	require.Equal(t, len(schedulerCanonicalBuckets(0))*2+4, cache.captureAttemptCount())
	require.Equal(t, expectedCanonicalProviderQueryCount()+2, providers.callCount())
	groups.mu.Lock()
	require.Equal(t, 1, groups.listCalls)
	groups.mu.Unlock()
	require.Empty(t, cache.retiredBuckets())
	require.Empty(t, cache.tokens())
	for _, bucket := range []SchedulerBucket{groupZeroHistorical, activeHistorical, activeForced, activeMixed, invalidHistorical} {
		_, published := cache.counts(bucket)
		require.Equal(t, 1, published, bucket.String())
	}
}

// TestSchedulerFullRebuildActiveTombstoneFreshInactiveOrMissingFiltersAllGroupTasks
// supplies inactive or missing fresh authority after discovery marked the
// group active. It checks retirement and zero provider reads or publications
// for that positive group, with no reopen tokens returned.
func TestSchedulerFullRebuildActiveTombstoneFreshInactiveOrMissingFiltersAllGroupTasks(t *testing.T) {
	for _, tc := range []struct {
		name string // fresh authority case label
		// group is omitted for the missing case, leaving the fresh map empty so
		// GetByIDLite returns the fixture's missing-group sentinel.
		group *SnapshotGroup // configured fresh authority group
	}{
		{name: "inactive", group: &SnapshotGroup{ID: 104, Status: StatusDisabled, Hydrated: true}},
		{name: "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const groupID int64 = 104
			canonical := schedulerBucketsForGroup(groupID)
			historical := SchedulerBucket{GroupID: groupID, Platform: "legacy", Mode: "unknown"}
			cache := newFullRebuildLifecycleCache(historical)
			require.NoError(t, cache.retirementRaceCache.RetireBucket(context.Background(), canonical[5]))
			groups := &fullRebuildLifecycleGroupRepo{
				activeIDs: []int64{groupID},
				fresh:     make(map[int64]*SnapshotGroup),
				freshErr:  make(map[int64]error),
			}
			if tc.group != nil {
				groups.fresh[groupID] = tc.group
			}
			providers := &fullRebuildProviderRepo{}
			svc := newFullRebuildLifecycleService(cache, nil, providers, groups)

			require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "test"))
			require.Zero(t, providers.groupCallCount(groupID))
			require.Zero(t, providers.groupCallCount(0))
			require.Empty(t, cache.tokens())
			require.Equal(t, bucketStrings(append(canonical, historical)), bucketStrings(cache.retiredBuckets()))
			for _, bucket := range append(canonical, historical) {
				attempts, published := cache.counts(bucket)
				require.Zero(t, attempts, bucket.String())
				require.Zero(t, published, bucket.String())
			}
			_, _, listCalls := cache.lifecycleCounts()
			require.Equal(t, 1, listCalls)
		})
	}
}

// TestSchedulerFullRebuildStaleCandidatesAreSortedAndNeverReopenedAcrossRounds
// presents inactive/missing groups in unsorted registry order across two
// rebuilds. It checks sorted fresh authority reads once, retained retirement
// counts, and no reopened tokens or provider queries for those groups.
func TestSchedulerFullRebuildStaleCandidatesAreSortedAndNeverReopenedAcrossRounds(t *testing.T) {
	historical := []SchedulerBucket{
		{GroupID: 3, Platform: "legacy", Mode: "unknown"},
		{GroupID: 1, Platform: "legacy", Mode: "unknown"},
		{GroupID: 2, Platform: "legacy", Mode: "unknown"},
	}
	cache := newFullRebuildLifecycleCache(historical...)
	groups := &fullRebuildLifecycleGroupRepo{
		fresh: map[int64]*SnapshotGroup{
			1: {ID: 1, Status: StatusDisabled, Hydrated: true},
			3: {ID: 3, Status: StatusDisabled, Hydrated: true},
		},
		freshErr: make(map[int64]error),
	}
	providers := &fullRebuildProviderRepo{}
	svc := newFullRebuildLifecycleService(cache, nil, providers, groups)

	require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "first"))
	retireCallsAfterFirst := len(cache.retiredBuckets())
	require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "second"))
	_, _, freshCalls := groups.stats()
	require.Equal(t, []int64{1, 2, 3}, freshCalls)
	require.Equal(t, retireCallsAfterFirst, len(cache.retiredBuckets()))
	require.Empty(t, cache.tokens())
	require.Zero(t, providers.groupCallCount(1))
	require.Zero(t, providers.groupCallCount(2))
	require.Zero(t, providers.groupCallCount(3))
	_, _, listCalls := cache.lifecycleCounts()
	require.Equal(t, 2, listCalls, "each round must use only its one global registry snapshot")
	for _, bucket := range historical {
		require.Contains(t, bucketStrings(cache.retiredBuckets()), bucket.String())
	}
}

// TestSchedulerFullRebuildPartialLifecycleFailureReturnsBeforeDBAndRetries
// fails authority after an earlier stale group was retired. It checks that
// partial retirement remains while provider work stops, then a corrected retry
// retires the remaining groups without reopening them.
func TestSchedulerFullRebuildPartialLifecycleFailureReturnsBeforeDBAndRetries(t *testing.T) {
	historical := []SchedulerBucket{
		{GroupID: 1, Platform: "legacy", Mode: "unknown"},
		{GroupID: 2, Platform: "legacy", Mode: "unknown"},
		{GroupID: 3, Platform: "legacy", Mode: "unknown"},
	}
	wantErr := errors.New("fresh group query failed")
	cache := newFullRebuildLifecycleCache(historical...)
	groups := &fullRebuildLifecycleGroupRepo{
		fresh: map[int64]*SnapshotGroup{
			1: {ID: 1, Status: StatusDisabled, Hydrated: true},
			3: {ID: 3, Status: StatusDisabled, Hydrated: true},
		},
		freshErr: map[int64]error{2: wantErr},
	}
	providers := &fullRebuildProviderRepo{}
	svc := newFullRebuildLifecycleService(cache, nil, providers, groups)

	err := svc.triggerFullRebuild("first")
	require.ErrorIs(t, err, wantErr)
	_, _, freshCalls := groups.stats()
	require.Equal(t, []int64{1, 2}, freshCalls)
	require.Zero(t, providers.callCount())
	require.Zero(t, cache.totalSetAttempts())
	retiredPerGroup := len(schedulerBucketsForGroup(1)) + 1
	require.Equal(t, retiredPerGroup, len(cache.retiredBuckets()))

	groups.mu.Lock()
	delete(groups.freshErr, 2)
	groups.fresh[2] = &SnapshotGroup{ID: 2, Status: StatusDisabled, Hydrated: true}
	groups.mu.Unlock()
	require.NoError(t, svc.triggerFullRebuild("retry"))
	_, _, freshCalls = groups.stats()
	require.Equal(t, []int64{1, 2, 2, 3}, freshCalls)
	require.Equal(t, retiredPerGroup*3, len(cache.retiredBuckets()))
	require.Zero(t, providers.callCount())
	require.Empty(t, cache.tokens())
}

// TestSchedulerFullRebuildActiveTombstoneLazyRecoveryDiscardsPartialCaptureTasks
// encounters a tombstone after partial canonical capture. It checks fresh
// reopening and release before source reads, one publication per canonical
// bucket, and successful historical publication without replaying the partial
// captures.
func TestSchedulerFullRebuildActiveTombstoneLazyRecoveryDiscardsPartialCaptureTasks(t *testing.T) {
	const groupID int64 = 105
	canonical := schedulerBucketsForGroup(groupID)
	historical := SchedulerBucket{GroupID: groupID, Platform: "legacy", Mode: "unknown"}
	cache := newFullRebuildLifecycleCache(canonical[0], canonical[4], historical)
	require.NoError(t, cache.retirementRaceCache.RetireBucket(context.Background(), canonical[5]))
	groups := &fullRebuildLifecycleGroupRepo{
		activeIDs: []int64{groupID},
		fresh: map[int64]*SnapshotGroup{
			groupID: {ID: groupID, Status: StatusActive, Hydrated: true},
		},
		freshErr: make(map[int64]error),
	}
	providers := &fullRebuildProviderRepo{}
	var capturesAtFirstDB int
	// Count initial failed captures and recovered reopen tokens before source
	// work. The hook exposes whether partial captures were discarded before the
	// provider query started.
	providers.beforeFirst = func() {
		capturesAtFirstDB = cache.captureAttemptCount()
		held, reopenCount := cache.leaseHeldAndTokenCount()
		require.False(t, held)
		require.Equal(t, len(canonical), reopenCount)
		require.Equal(t, len(schedulerCanonicalBuckets(0))+7, capturesAtFirstDB)
	}
	svc := newFullRebuildLifecycleService(cache, nil, providers, groups)

	require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "test"))
	require.Equal(t, capturesAtFirstDB, cache.captureAttemptCount())
	require.Equal(t, expectedCanonicalProviderQueryCount()+1, providers.callCount())
	for _, bucket := range canonical {
		attempts, published := cache.counts(bucket)
		require.Equal(t, 1, attempts, "discarded pre-recovery tokens must never publish: %s", bucket.String())
		require.Equal(t, 1, published, bucket.String())
	}
	_, published := cache.counts(historical)
	require.Equal(t, 1, published)
	_, _, listCalls := cache.lifecycleCounts()
	require.Equal(t, 1, listCalls)
}

// TestSchedulerFullRebuildUsesGroupLifecycleAuthority combines zero, positive,
// and negative historical registry entries with no active discovery IDs. It
// checks that only the positive group receives fresh authority and retirement
// while zero/negative identities remain ordinary empty rebuild work.
func TestSchedulerFullRebuildUsesGroupLifecycleAuthority(t *testing.T) {
	registered := []SchedulerBucket{{GroupID: 0, Platform: "legacy-zero", Mode: "unknown"}, {GroupID: 106, Platform: "legacy-positive", Mode: "unknown"}, {GroupID: -8, Platform: "legacy-negative", Mode: "unknown"}}
	cache := newFullRebuildLifecycleCache(registered...)
	groups := &fullRebuildLifecycleGroupRepo{fresh: make(map[int64]*SnapshotGroup), freshErr: make(map[int64]error)}
	providers := &fullRebuildProviderRepo{}
	svc := newFullRebuildLifecycleService(cache, nil, providers, groups)
	require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "test"))
	activeCalls, fallbackCalls, freshCalls := groups.stats()
	require.Equal(t, 1, activeCalls)
	require.Zero(t, fallbackCalls)
	require.Equal(t, []int64{106}, freshCalls)
	require.Equal(t, len(schedulerCanonicalBuckets(0))+2, cache.captureAttemptCount())
	require.Zero(t, providers.callCount())
	require.Equal(t, bucketStrings(append(schedulerCanonicalBuckets(106), registered[1])), bucketStrings(cache.retiredBuckets()))
	require.Empty(t, cache.tokens())
}

// TestSchedulerFullRebuildFreshReopenLockBusyRetriesWithoutBlockingOrdinaryTasks
// makes the first strict reopened bucket busy once. It checks retained
// watermark with ordinary group-zero publication on failure, then a second
// poll's successful bucket publication and watermark advancement.
func TestSchedulerFullRebuildFreshReopenLockBusyRetriesWithoutBlockingOrdinaryTasks(t *testing.T) {
	const groupID int64 = 107
	canonical := schedulerBucketsForGroup(groupID)
	cache := newFullRebuildLifecycleCache()
	require.NoError(t, cache.retirementRaceCache.RetireBucket(context.Background(), canonical[0]))
	cache.lockBusyOnce[canonical[0].String()] = true
	groups := &fullRebuildLifecycleGroupRepo{
		activeIDs: []int64{groupID},
		fresh: map[int64]*SnapshotGroup{
			groupID: {ID: groupID, Status: StatusActive, Hydrated: true},
		},
		freshErr: make(map[int64]error),
	}
	providers := &fullRebuildProviderRepo{}
	outbox := &outboxCleanupRepo{events: []SchedulerOutboxEvent{{ID: 1, EventType: SchedulerOutboxEventFullRebuild}}}
	svc := newFullRebuildLifecycleService(cache, outbox, providers, groups)

	svc.pollOutbox()
	require.Zero(t, cache.currentWatermark())
	_, groupZeroPublished := cache.counts(schedulerCanonicalBuckets(0)[0])
	require.Equal(t, 1, groupZeroPublished, "ordinary tasks must still run when one strict Reopen task is busy")
	require.Equal(t, expectedCanonicalProviderQueryCount(), providers.callCount())

	svc.pollOutbox()
	require.Equal(t, int64(1), cache.currentWatermark())
	require.Equal(t, expectedCanonicalProviderQueryCount()*2, providers.callCount())
	_, busyBucketPublished := cache.counts(canonical[0])
	require.Equal(t, 1, busyBucketPublished)
	activeCalls, fallbackCalls, freshCalls := groups.stats()
	require.Equal(t, 2, activeCalls)
	require.Zero(t, fallbackCalls)
	require.Equal(t, []int64{groupID}, freshCalls)
}

// TestSchedulerFullRebuildOrdinaryLockBusyKeepsExistingSkipSemantics makes a
// canonical group-zero ordinary bucket busy once. The rebuild must still
// succeed while that bucket records no publication and the provider source
// remains unread.
func TestSchedulerFullRebuildOrdinaryLockBusyKeepsExistingSkipSemantics(t *testing.T) {
	busyBucket := schedulerCanonicalBuckets(0)[0]
	cache := newFullRebuildLifecycleCache()
	cache.lockBusyOnce[busyBucket.String()] = true
	groups := &fullRebuildLifecycleGroupRepo{fresh: make(map[int64]*SnapshotGroup), freshErr: make(map[int64]error)}
	providers := &fullRebuildProviderRepo{}
	svc := newFullRebuildLifecycleService(cache, nil, providers, groups)

	require.NoError(t, svc.rebuildFullSnapshot(context.Background(), "test"))
	require.Zero(t, providers.callCount())
	attempts, published := cache.counts(busyBucket)
	require.Zero(t, attempts)
	require.Zero(t, published)
}

// TestSchedulerSnapshotServiceFullRebuildCoalescesConcurrentRequestsIntoTrailingRun
// holds the first run while twenty followers register. It checks exactly two
// serialized runs, first-call success, a shared trailing failure for
// followers, and completion covering their recorded generations.
func TestSchedulerSnapshotServiceFullRebuildCoalescesConcurrentRequestsIntoTrailingRun(t *testing.T) {
	svc := &SnapshotService{}
	wantTrailingErr := errors.New("trailing rebuild failed")
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	// The barrier can be opened explicitly and by deferred failure cleanup. A
	// once guard prevents double close while allowing blocked work to finish
	// after failed assertions.
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseFirst) })
	}
	defer release()

	var calls atomic.Int32
	var active atomic.Int32
	var maxActive atomic.Int32
	run := func() error {
		call := calls.Add(1)
		currentActive := active.Add(1)
		defer active.Add(-1)
		// Maintain the maximum simultaneous run count with an atomic
		// compare-and-swap loop, so concurrent observations cannot overwrite a
		// larger peak already recorded.
		for {
			previousMax := maxActive.Load()
			if currentActive <= previousMax || maxActive.CompareAndSwap(previousMax, currentActive) {
				break
			}
		}
		if call == 1 {
			close(firstStarted)
			<-releaseFirst
			return nil
		}
		return wantTrailingErr
	}

	firstResult := make(chan error, 1)
	// Hold the first run after generation coverage is captured, then register
	// followers while it remains blocked. Their requests must belong to a
	// trailing run rather than that first read.
	go func() {
		firstResult <- svc.coalesceFullRebuild(run)
	}()

	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first rebuild did not start")
	}

	const followers = 20
	followerResults := make(chan error, followers)
	for range followers {
		go func() {
			followerResults <- svc.coalesceFullRebuild(run)
		}()
	}

	// Wait until every follower has recorded its generation before opening the
	// first barrier. This enrollment check makes trailing coverage independent of
	// goroutine scheduling order.
	require.Eventually(t, func() bool {
		requested, _ := schedulerFullRebuildState(svc)
		return requested == followers+1
	}, time.Second, time.Millisecond)
	release()

	require.NoError(t, <-firstResult)
	for range followers {
		require.ErrorIs(t, <-followerResults, wantTrailingErr)
	}
	require.EqualValues(t, 2, calls.Load())
	require.EqualValues(t, 1, maxActive.Load())
	requested, completed := schedulerFullRebuildState(svc)
	require.EqualValues(t, followers+1, requested)
	require.Equal(t, requested, completed)
}

// TestSchedulerSnapshotServiceFullRebuildRunsAgainForSequentialRequest issues
// two completed requests sequentially. It checks a separate second run and its
// failure, with requested and completed generations both advancing to two.
func TestSchedulerSnapshotServiceFullRebuildRunsAgainForSequentialRequest(t *testing.T) {
	svc := &SnapshotService{}
	wantSecondErr := errors.New("second rebuild failed")
	var calls atomic.Int32
	run := func() error {
		if calls.Add(1) == 2 {
			return wantSecondErr
		}
		return nil
	}

	require.NoError(t, svc.coalesceFullRebuild(run))
	require.ErrorIs(t, svc.coalesceFullRebuild(run), wantSecondErr)
	require.EqualValues(t, 2, calls.Load())
	requested, completed := schedulerFullRebuildState(svc)
	require.EqualValues(t, 2, requested)
	require.Equal(t, requested, completed)
}

// TestSchedulerSnapshotServiceInitialFullRebuildFailsClosedWhenListBucketsFails
// makes startup registry discovery fail. It checks one discovery, zero
// capture/lock work, and a completed coalesced generation retaining that same
// error.
func TestSchedulerSnapshotServiceInitialFullRebuildFailsClosedWhenListBucketsFails(t *testing.T) {
	cache := &schedulerFullRebuildTestCache{listErr: errors.New("list buckets failed")}
	svc := NewSnapshotService(cache, nil, nil, nil, nil)

	svc.runInitialRebuild()

	cache.mu.Lock()
	listCalls := cache.listCalls
	captures := cache.captures
	lockCalls := cache.lockCalls
	cache.mu.Unlock()
	require.Equal(t, 1, listCalls)
	require.Zero(t, captures)
	require.Zero(t, lockCalls)
	requested, completed := schedulerFullRebuildState(svc)
	require.EqualValues(t, 1, requested)
	require.Equal(t, requested, completed)
	svc.fullRebuildStateMu.Lock()
	require.ErrorIs(t, svc.fullRebuildLastErr, cache.listErr)
	svc.fullRebuildStateMu.Unlock()
}

// TestSchedulerGroupLifecycleInactiveAndMissingRetireAllHistoricalBucketsWithoutProviderReads
// supplies inactive or missing group authority with historical buckets. It
// checks leased retirement of that group's canonical/history set, exclusion of
// other/zero groups, no provider reads, and completed lifecycle markers.
func TestSchedulerGroupLifecycleInactiveAndMissingRetireAllHistoricalBucketsWithoutProviderReads(t *testing.T) {
	for _, tc := range []struct {
		// name, group, and err define one fresh authority result for this case. An
		// inactive value or missing-record error drives retirement before provider
		// queries.
		name  string         // inactive authority case label
		group *SnapshotGroup // configured inactive authority value
		err   error          // configured missing authority error
	}{
		{name: "inactive", group: &SnapshotGroup{ID: 81, Status: StatusDisabled, Hydrated: true}},
		{name: "missing", err: ErrSnapshotGroupNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const groupID int64 = 81
			current := expectedGroupLifecycleBuckets(groupID)
			historical := SchedulerBucket{GroupID: groupID, Platform: "legacy", Mode: "obsolete"}
			other := SchedulerBucket{GroupID: groupID + 1, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
			groupZero := SchedulerBucket{GroupID: 0, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
			cache := newGroupLifecycleTestCache(current[0], historical, other, groupZero)
			groups := &groupLifecycleTestGroupRepo{group: tc.group, err: tc.err}
			providers := &groupLifecycleTestProviderRepo{}
			svc := newGroupLifecycleTestService(cache, providers, groups)
			seen := make(map[batchSeenKey]struct{})

			require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(groupID), seen))

			expected := bucketStrings(append(current, historical))
			got := bucketStrings(cache.retiredBuckets())
			require.Equal(t, expected, got)
			retireHeld, _ := cache.lifecycleMutationLeaseStates()
			require.Len(t, retireHeld, len(expected))
			for _, held := range retireHeld {
				require.True(t, held)
			}
			require.NotContains(t, got, other.String())
			require.NotContains(t, got, groupZero.String())
			require.Zero(t, providers.callCount())
			require.Equal(t, 1, groups.callCount())
			_, _, listCalls := cache.lifecycleCounts()
			require.Equal(t, 1, listCalls)
			requireLifecycleSeen(t, seen, groupID)
		})
	}
}

// TestSchedulerPrepareGroupLifecycleUsesKnownHistoricalBucketsWithoutListingRegistry
// supplies discovered history alongside a deliberately failing registry
// reader. It checks inactive preparation retires that history without another
// registry read, provider query, or publication task.
func TestSchedulerPrepareGroupLifecycleUsesKnownHistoricalBucketsWithoutListingRegistry(t *testing.T) {
	const groupID int64 = 811
	historical := SchedulerBucket{GroupID: groupID, Platform: "legacy", Mode: "obsolete"}
	cache := newGroupLifecycleTestCache()
	cache.listErr = errors.New("registry must not be listed")
	groups := &groupLifecycleTestGroupRepo{group: &SnapshotGroup{ID: groupID, Status: StatusDisabled, Hydrated: true}}
	providers := &groupLifecycleTestProviderRepo{}
	svc := newGroupLifecycleTestService(cache, providers, groups)

	plan, err := svc.prepareGroupLifecycle(context.Background(), groupID, []SchedulerBucket{historical})
	require.NoError(t, err)
	require.False(t, plan.active)
	require.Empty(t, plan.tasks)
	_, _, listCalls := cache.lifecycleCounts()
	require.Zero(t, listCalls)
	require.Contains(t, bucketStrings(cache.retiredBuckets()), historical.String())
	require.Zero(t, providers.callCount())
}

// TestSchedulerGroupLifecycleActiveReopensAndRebuildsAllCurrentBuckets starts
// with retired canonical buckets and active authority. It checks leased
// reopen, every token before the first provider read, owner release before
// queries, shared query counts, publication, deadlines, bucket release counts,
// and lifecycle completion.
func TestSchedulerGroupLifecycleActiveReopensAndRebuildsAllCurrentBuckets(t *testing.T) {
	const groupID int64 = 82
	current := expectedGroupLifecycleBuckets(groupID)
	historical := SchedulerBucket{GroupID: groupID, Platform: "legacy", Mode: "obsolete"}
	cache := newGroupLifecycleTestCache(historical)
	for _, bucket := range current {
		require.NoError(t, cache.retirementRaceCache.RetireBucket(context.Background(), bucket))
	}
	groups := &groupLifecycleTestGroupRepo{group: &SnapshotGroup{ID: groupID, Status: StatusActive, Hydrated: true}}
	providers := &groupLifecycleTestProviderRepo{}
	// Observe the first lifecycle source read after all canonical reopen
	// decisions. The owner flag must already be clear so a later retirement can
	// run while provider work proceeds.
	providers.beforeLoad = func() {
		held, tokenCount := cache.leaseHeldAndTokenCount()
		require.False(t, held, "the group lifecycle lease must be released before the first provider query")
		require.Equal(t, len(current), tokenCount, "all reopen tokens must be prepared before the first provider query")
	}
	svc := newGroupLifecycleTestService(cache, providers, groups)
	seen := make(map[batchSeenKey]struct{})

	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(groupID), seen))

	require.Equal(t, bucketStrings(current), bucketStrings(cache.reopens))
	require.Empty(t, cache.retiredBuckets())
	registered, err := cache.retirementRaceCache.ListBuckets(context.Background())
	require.NoError(t, err)
	require.Contains(t, bucketStrings(registered), historical.String())
	require.Len(t, cache.tokens(), len(current))
	require.Equal(t, expectedCanonicalProviderQueryCount(), providers.callCount())
	require.Equal(t, 1, providers.platformCallCount(PlatformOpenAI))
	for _, bucket := range current {
		_, published := cache.counts(bucket)
		require.Equal(t, 1, published, bucket.String())
	}
	require.Contains(t, bucketStrings(current), SchedulerBucket{GroupID: groupID, Platform: PlatformAntigravity, Mode: SchedulerModeForced}.String())
	require.Contains(t, bucketStrings(current), SchedulerBucket{GroupID: groupID, Platform: "", Mode: SchedulerModeSingle}.String())
	require.Contains(t, bucketStrings(current), SchedulerBucket{GroupID: groupID, Platform: "", Mode: SchedulerModeForced}.String())
	acquires, releases, listCalls := cache.lifecycleCounts()
	require.Equal(t, 1, acquires)
	require.Equal(t, 1, releases)
	require.Zero(t, listCalls)
	require.Equal(t, schedulerGroupLifecycleLeaseTTL, cache.acquireTTL)
	require.True(t, cache.acquireDeadline)
	require.True(t, cache.releaseDeadline)
	require.NoError(t, cache.releaseCtxErr)
	_, reopenHeld := cache.lifecycleMutationLeaseStates()
	require.Len(t, reopenHeld, len(current))
	for _, held := range reopenHeld {
		require.True(t, held)
	}
	lockTTLs, unlockCalls := cache.lockStats()
	require.Len(t, lockTTLs, len(current))
	for _, ttl := range lockTTLs {
		require.Equal(t, 30*time.Second, ttl)
	}
	require.Equal(t, len(current), unlockCalls)
	requireLifecycleSeen(t, seen, groupID)
}

// TestSchedulerGroupLifecycleInactiveThenActiveAuthoritativelyReopens retires
// an inactive group, then changes its fresh authority to active. It checks no
// inactive provider reads and a complete later reopen/query/publication set
// for the canonical buckets.
func TestSchedulerGroupLifecycleInactiveThenActiveAuthoritativelyReopens(t *testing.T) {
	const groupID int64 = 83
	cache := newGroupLifecycleTestCache()
	groups := &groupLifecycleTestGroupRepo{group: &SnapshotGroup{ID: groupID, Status: StatusDisabled, Hydrated: true}}
	providers := &groupLifecycleTestProviderRepo{}
	svc := newGroupLifecycleTestService(cache, providers, groups)

	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(groupID), make(map[batchSeenKey]struct{})))
	require.Zero(t, providers.callCount())
	groups.set(&SnapshotGroup{ID: groupID, Status: StatusActive, Hydrated: true}, nil)
	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(groupID), make(map[batchSeenKey]struct{})))

	require.Len(t, cache.tokens(), len(expectedGroupLifecycleBuckets(groupID)))
	require.Equal(t, expectedCanonicalProviderQueryCount(), providers.callCount())
	for _, bucket := range expectedGroupLifecycleBuckets(groupID) {
		_, published := cache.counts(bucket)
		require.Equal(t, 1, published, bucket.String())
	}
}

// TestSchedulerGroupLifecycleLaterInactiveFencesLongActiveRebuild pauses an
// active group's provider query before handling inactive authority separately.
// It checks that retirement completes while the resumed active publication
// returns retired and remains unmarked.
func TestSchedulerGroupLifecycleLaterInactiveFencesLongActiveRebuild(t *testing.T) {
	const groupID int64 = 84
	cache := newGroupLifecycleTestCache()
	groups := &groupLifecycleTestGroupRepo{group: &SnapshotGroup{ID: groupID, Status: StatusActive, Hydrated: true}}
	started := make(chan struct{})
	release := make(chan struct{})
	providers := &groupLifecycleTestProviderRepo{started: started, release: release}
	svc := newGroupLifecycleTestService(cache, providers, groups)
	activeSeen := make(map[batchSeenKey]struct{})
	inactiveSeen := make(map[batchSeenKey]struct{})
	activeResult := make(chan error, 1)

	// Pause the active event at its provider query after owner release. A
	// separate inactive event can then retire its prepared tokens before the
	// first event resumes publication.
	go func() {
		activeResult <- svc.handleGroupEvent(context.Background(), ptrInt64(groupID), activeSeen)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("active rebuild did not reach the provider load")
	}

	groups.set(&SnapshotGroup{ID: groupID, Status: StatusDisabled, Hydrated: true}, nil)
	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(groupID), inactiveSeen))
	// Inactive authority has completed retirement before this barrier opens. Join
	// the old event's result before inspecting its markers so no concurrent map
	// write remains.
	close(release)
	err := <-activeResult
	require.ErrorIs(t, err, ErrSchedulerBucketRetired)
	requireLifecycleNotSeen(t, activeSeen, groupID)
	requireLifecycleSeen(t, inactiveSeen, groupID)
}

// TestSchedulerGroupLifecycleEpochPreventsABA performs inactive-to-active
// transitions twice and retains the first active token. It checks a later
// epoch increase and rejects that earlier token after the second reopen.
func TestSchedulerGroupLifecycleEpochPreventsABA(t *testing.T) {
	const groupID int64 = 85
	cache := newGroupLifecycleTestCache()
	groups := &groupLifecycleTestGroupRepo{group: &SnapshotGroup{ID: groupID, Status: StatusDisabled, Hydrated: true}}
	providers := &groupLifecycleTestProviderRepo{}
	svc := newGroupLifecycleTestService(cache, providers, groups)

	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(groupID), make(map[batchSeenKey]struct{})))
	groups.set(&SnapshotGroup{ID: groupID, Status: StatusActive, Hydrated: true}, nil)
	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(groupID), make(map[batchSeenKey]struct{})))
	firstActiveTokens := cache.tokens()
	canonicalBucketCount := len(expectedGroupLifecycleBuckets(groupID))
	require.Len(t, firstActiveTokens, canonicalBucketCount)

	groups.set(&SnapshotGroup{ID: groupID, Status: StatusDisabled, Hydrated: true}, nil)
	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(groupID), make(map[batchSeenKey]struct{})))
	groups.set(&SnapshotGroup{ID: groupID, Status: StatusActive, Hydrated: true}, nil)
	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(groupID), make(map[batchSeenKey]struct{})))
	allTokens := cache.tokens()
	require.Len(t, allTokens, canonicalBucketCount*2)
	require.Greater(t, allTokens[canonicalBucketCount].Epoch, firstActiveTokens[0].Epoch)
	require.ErrorIs(t, cache.SetSnapshot(context.Background(), firstActiveTokens[0].Bucket, firstActiveTokens[0], nil), ErrSchedulerBucketWriteFenced)
}

// TestSchedulerGroupLifecycleSeenIsIndependentAndDeduplicatesGroupEvents
// prepopulates ordinary platform markers for an active group. It checks one
// fresh lifecycle/query execution still occurs, then completed lifecycle
// markers suppress a repeated group event.
func TestSchedulerGroupLifecycleSeenIsIndependentAndDeduplicatesGroupEvents(t *testing.T) {
	const groupID int64 = 86
	cache := newGroupLifecycleTestCache()
	groups := &groupLifecycleTestGroupRepo{group: &SnapshotGroup{ID: groupID, Status: StatusActive, Hydrated: true}}
	providers := &groupLifecycleTestProviderRepo{}
	svc := newGroupLifecycleTestService(cache, providers, groups)
	seen := make(map[batchSeenKey]struct{})
	for _, platform := range schedulerSnapshotPlatforms() {
		seen[batchSeenKey{groupID: groupID, platform: platform}] = struct{}{}
	}

	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(groupID), seen))
	require.Equal(t, 1, groups.callCount())
	require.Equal(t, expectedCanonicalProviderQueryCount(), providers.callCount())
	requireLifecycleSeen(t, seen, groupID)
	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(groupID), seen))
	require.Equal(t, 1, groups.callCount())
	require.Equal(t, expectedCanonicalProviderQueryCount(), providers.callCount())
}

// TestSchedulerGroupLifecycleFailuresDoNotMarkSeen injects owner, authority,
// mutation, provider, bucket, and publication failures separately. It checks
// the expected cause, absent lifecycle/platform markers, and applicable
// cleanup or no-provider-work assertions for each failed phase.
func TestSchedulerGroupLifecycleFailuresDoNotMarkSeen(t *testing.T) {
	tests := []struct {
		// name, prepare, and check define each isolated failure case. prepare
		// configures fresh fixtures before handleGroupEvent; check inspects the
		// returned phase error before shared completion/cleanup assertions.
		name string // failed lifecycle phase label
		// case fixture failure setup
		prepare func(*groupLifecycleTestCache, *groupLifecycleTestGroupRepo, *groupLifecycleTestProviderRepo)
		check   func(*testing.T, error) // returned failure assertion callback
	}{
		{
			name: "lease busy",
			prepare: func(cache *groupLifecycleTestCache, _ *groupLifecycleTestGroupRepo, _ *groupLifecycleTestProviderRepo) {
				cache.leaseBusy = true
			},
			check: func(t *testing.T, err error) { require.ErrorIs(t, err, ErrSchedulerGroupLifecycleLeaseBusy) },
		},
		{
			name: "lease error",
			prepare: func(cache *groupLifecycleTestCache, _ *groupLifecycleTestGroupRepo, _ *groupLifecycleTestProviderRepo) {
				cache.leaseAcquireErr = errors.New("lease failed")
			},
			check: func(t *testing.T, err error) { require.EqualError(t, err, "lease failed") },
		},
		{
			name: "release lost",
			prepare: func(cache *groupLifecycleTestCache, _ *groupLifecycleTestGroupRepo, _ *groupLifecycleTestProviderRepo) {
				cache.leaseReleaseErr = ErrSchedulerGroupLifecycleLeaseLost
			},
			check: func(t *testing.T, err error) { require.ErrorIs(t, err, ErrSchedulerGroupLifecycleLeaseLost) },
		},
		{
			name: "release error",
			prepare: func(cache *groupLifecycleTestCache, _ *groupLifecycleTestGroupRepo, _ *groupLifecycleTestProviderRepo) {
				cache.leaseReleaseErr = errors.New("release failed")
			},
			check: func(t *testing.T, err error) { require.EqualError(t, err, "release failed") },
		},
		{
			name: "group query error",
			prepare: func(_ *groupLifecycleTestCache, groups *groupLifecycleTestGroupRepo, _ *groupLifecycleTestProviderRepo) {
				groups.err = errors.New("group query failed")
			},
			check: func(t *testing.T, err error) { require.EqualError(t, err, "group query failed") },
		},
		{
			name: "list buckets error",
			prepare: func(cache *groupLifecycleTestCache, groups *groupLifecycleTestGroupRepo, _ *groupLifecycleTestProviderRepo) {
				groups.group.Status = StatusDisabled
				cache.listErr = errors.New("list buckets failed")
			},
			check: func(t *testing.T, err error) { require.EqualError(t, err, "list buckets failed") },
		},
		{
			name: "retire bucket error",
			prepare: func(cache *groupLifecycleTestCache, groups *groupLifecycleTestGroupRepo, _ *groupLifecycleTestProviderRepo) {
				groups.group.Status = StatusDisabled
				cache.retireErr = errors.New("retire bucket failed")
				cache.retireErrAt = 2
			},
			check: func(t *testing.T, err error) { require.EqualError(t, err, "retire bucket failed") },
		},
		{
			name: "reopen bucket error",
			prepare: func(cache *groupLifecycleTestCache, _ *groupLifecycleTestGroupRepo, _ *groupLifecycleTestProviderRepo) {
				cache.reopenErr = errors.New("reopen bucket failed")
				cache.reopenErrAt = 2
			},
			check: func(t *testing.T, err error) { require.EqualError(t, err, "reopen bucket failed") },
		},
		{
			name: "provider rebuild error",
			prepare: func(_ *groupLifecycleTestCache, _ *groupLifecycleTestGroupRepo, providers *groupLifecycleTestProviderRepo) {
				providers.err = errors.New("provider load failed")
			},
			check: func(t *testing.T, err error) { require.EqualError(t, err, "provider load failed") },
		},
		{
			name: "bucket lock busy",
			prepare: func(cache *groupLifecycleTestCache, _ *groupLifecycleTestGroupRepo, _ *groupLifecycleTestProviderRepo) {
				cache.bucketLockBusy = true
			},
			check: func(t *testing.T, err error) { require.ErrorIs(t, err, ErrSchedulerBucketRebuildBusy) },
		},
		{
			name: "bucket lock error",
			prepare: func(cache *groupLifecycleTestCache, _ *groupLifecycleTestGroupRepo, _ *groupLifecycleTestProviderRepo) {
				cache.bucketLockErr = errors.New("bucket lock failed")
			},
			check: func(t *testing.T, err error) { require.EqualError(t, err, "bucket lock failed") },
		},
		{
			name: "set snapshot error",
			prepare: func(cache *groupLifecycleTestCache, _ *groupLifecycleTestGroupRepo, _ *groupLifecycleTestProviderRepo) {
				cache.setErr = errors.New("set snapshot failed")
			},
			check: func(t *testing.T, err error) { require.EqualError(t, err, "set snapshot failed") },
		},
	}

	for index, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			groupID := int64(870 + index)
			cache := newGroupLifecycleTestCache()
			groups := &groupLifecycleTestGroupRepo{group: &SnapshotGroup{ID: groupID, Status: StatusActive, Hydrated: true}}
			providers := &groupLifecycleTestProviderRepo{}
			tc.prepare(cache, groups, providers)
			svc := newGroupLifecycleTestService(cache, providers, groups)
			seen := make(map[batchSeenKey]struct{})

			err := svc.handleGroupEvent(context.Background(), ptrInt64(groupID), seen)
			tc.check(t, err)
			requireLifecycleNotSeen(t, seen, groupID)
			if tc.name == "release lost" || tc.name == "release error" {
				require.Zero(t, providers.callCount())
			}
			if tc.name == "retire bucket error" || tc.name == "reopen bucket error" {
				_, releases, _ := cache.lifecycleCounts()
				require.Equal(t, 1, releases)
				require.Zero(t, providers.callCount())
			}
			if tc.name == "provider rebuild error" || tc.name == "set snapshot error" {
				lockTTLs, unlockCalls := cache.lockStats()
				require.Len(t, lockTTLs, 1)
				require.Equal(t, 1, unlockCalls)
				require.Equal(t, 1, providers.callCount())
			}
		})
	}
}

// TestSchedulerGroupLifecycleOperationAndReleaseErrorsPreserveBothCauses
// combines authority failure with lost owner release. It checks both errors
// remain discoverable, lifecycle work remains unmarked, and no provider source
// query runs.
func TestSchedulerGroupLifecycleOperationAndReleaseErrorsPreserveBothCauses(t *testing.T) {
	const groupID int64 = 880
	operationErr := errors.New("group query failed")
	cache := newGroupLifecycleTestCache()
	cache.leaseReleaseErr = ErrSchedulerGroupLifecycleLeaseLost
	groups := &groupLifecycleTestGroupRepo{err: operationErr}
	providers := &groupLifecycleTestProviderRepo{}
	svc := newGroupLifecycleTestService(cache, providers, groups)
	seen := make(map[batchSeenKey]struct{})

	err := svc.handleGroupEvent(context.Background(), ptrInt64(groupID), seen)
	require.ErrorIs(t, err, operationErr)
	require.ErrorIs(t, err, ErrSchedulerGroupLifecycleLeaseLost)
	requireLifecycleNotSeen(t, seen, groupID)
	require.Zero(t, providers.callCount())
}

// TestSchedulerGroupLifecycleUntrustedGroupStateFailsClosed supplies
// unhydrated or mismatched fresh authority. It checks rejection before
// retire/reopen/registry/provider work, while acquired lifecycle ownership is
// released and completion markers stay absent.
func TestSchedulerGroupLifecycleUntrustedGroupStateFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string         // untrusted authority case label
		group *SnapshotGroup // mismatched or unhydrated authority
	}{
		{name: "not hydrated", group: &SnapshotGroup{ID: 88, Status: StatusActive}},
		{name: "mismatched id", group: &SnapshotGroup{ID: 89, Status: StatusActive, Hydrated: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const eventGroupID int64 = 88
			cache := newGroupLifecycleTestCache()
			groups := &groupLifecycleTestGroupRepo{group: tc.group}
			providers := &groupLifecycleTestProviderRepo{}
			svc := newGroupLifecycleTestService(cache, providers, groups)
			seen := make(map[batchSeenKey]struct{})

			err := svc.handleGroupEvent(context.Background(), ptrInt64(eventGroupID), seen)
			require.Error(t, err)
			require.Empty(t, cache.retiredBuckets())
			require.Empty(t, cache.tokens())
			require.Zero(t, providers.callCount())
			requireLifecycleNotSeen(t, seen, eventGroupID)
			acquires, releases, listCalls := cache.lifecycleCounts()
			require.Equal(t, 1, acquires)
			require.Equal(t, 1, releases)
			require.Zero(t, listCalls)
		})
	}
}

// TestSchedulerGroupLifecycleCanceledAfterFreshQueryUsesIndependentReleaseContext
// cancels immediately after the authority value is copied. It checks canceled
// work, no reopened tokens or provider reads, and one deadline-bounded release
// using a live independent context.
func TestSchedulerGroupLifecycleCanceledAfterFreshQueryUsesIndependentReleaseContext(t *testing.T) {
	const groupID int64 = 89
	ctx, cancel := context.WithCancel(context.Background())
	cache := newGroupLifecycleTestCache()
	// Cancel only after the authority value was copied. Reopen then sees the
	// ended operation context while owner cleanup receives the independent
	// background deadline.
	groups := &groupLifecycleTestGroupRepo{
		group:    &SnapshotGroup{ID: groupID, Status: StatusActive, Hydrated: true},
		afterGet: cancel,
	}
	providers := &groupLifecycleTestProviderRepo{}
	svc := newGroupLifecycleTestService(cache, providers, groups)
	seen := make(map[batchSeenKey]struct{})

	err := svc.handleGroupEvent(ctx, ptrInt64(groupID), seen)
	require.ErrorIs(t, err, context.Canceled)
	requireLifecycleNotSeen(t, seen, groupID)
	require.Empty(t, cache.tokens())
	require.Zero(t, providers.callCount())
	acquires, releases, _ := cache.lifecycleCounts()
	require.Equal(t, 1, acquires)
	require.Equal(t, 1, releases)
	require.True(t, cache.releaseDeadline)
	require.NoError(t, cache.releaseCtxErr)
}

// TestSchedulerGroupLifecycleUsesExplicitGroups issues nil, zero, then
// positive group-change IDs. It checks that only the positive ID causes
// authority acquisition, release, and the canonical provider query set.
func TestSchedulerGroupLifecycleUsesExplicitGroups(t *testing.T) {
	cache := newGroupLifecycleTestCache()
	groups := &groupLifecycleTestGroupRepo{group: &SnapshotGroup{ID: 88, Status: StatusActive, Hydrated: true}}
	providers := &groupLifecycleTestProviderRepo{}
	svc := newGroupLifecycleTestService(cache, providers, groups)

	require.NoError(t, svc.handleGroupEvent(context.Background(), nil, make(map[batchSeenKey]struct{})))
	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(0), make(map[batchSeenKey]struct{})))
	require.NoError(t, svc.handleGroupEvent(context.Background(), ptrInt64(88), make(map[batchSeenKey]struct{})))

	acquires, releases, listCalls := cache.lifecycleCounts()
	require.Equal(t, 1, acquires)
	require.Equal(t, 1, releases)
	require.Zero(t, listCalls)
	require.Equal(t, 1, groups.callCount())
	require.Equal(t, expectedCanonicalProviderQueryCount(), providers.callCount())
}

// TestSchedulerSnapshotServicePollOutboxCleansConsumedRowsAfterWatermark
// consumes ID10000 with older fixture rows still present. It checks one
// watermark write, leased deletion in two full chunks plus a short pass, and
// preservation of rows above the watermark.
func TestSchedulerSnapshotServicePollOutboxCleansConsumedRowsAfterWatermark(t *testing.T) {
	cache := &outboxCleanupCache{}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{
			{ID: 10000, EventType: SchedulerOutboxEventProviderLastUsed},
		},
		rows:         int64Range(1, 10003),
		lockAcquired: true,
	}
	svc := NewSnapshotService(cache, repo, nil, nil, nil)

	svc.pollOutbox()

	if cache.watermark != 10000 {
		t.Fatalf("expected watermark 10000, got %d", cache.watermark)
	}
	if !reflect.DeepEqual(cache.setWatermarks, []int64{10000}) {
		t.Fatalf("unexpected watermark writes: %#v", cache.setWatermarks)
	}
	if !reflect.DeepEqual(repo.rows, []int64{10001, 10002, 10003}) {
		t.Fatalf("expected rows above watermark to remain, got %#v", repo.rows)
	}
	if repo.lockAttempts != 1 || repo.releaseCount != 1 {
		t.Fatalf("expected one lock acquire/release, got acquire=%d release=%d", repo.lockAttempts, repo.releaseCount)
	}
	if len(repo.deleteCalls) != 3 {
		t.Fatalf("expected cleanup to loop until a short batch, got %d calls", len(repo.deleteCalls))
	}
	for _, call := range repo.deleteCalls {
		if call.watermark != 10000 || call.limit != schedulerOutboxCleanupBatch {
			t.Fatalf("unexpected cleanup call: %#v", call)
		}
	}
}

// TestSchedulerSnapshotServicePollOutboxSkipsCleanupWhenLockUnavailable denies
// cleanup ownership after a visible event is handled. It checks watermark
// advancement with all rows retained, one acquisition attempt, and no deletion
// or release.
func TestSchedulerSnapshotServicePollOutboxSkipsCleanupWhenLockUnavailable(t *testing.T) {
	cache := &outboxCleanupCache{}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{
			{ID: 3, EventType: SchedulerOutboxEventProviderLastUsed},
		},
		rows:         []int64{1, 2, 3, 4},
		lockAcquired: false,
	}
	svc := NewSnapshotService(cache, repo, nil, nil, nil)

	svc.pollOutbox()

	if cache.watermark != 3 {
		t.Fatalf("expected watermark 3, got %d", cache.watermark)
	}
	if !reflect.DeepEqual(repo.rows, []int64{1, 2, 3, 4}) {
		t.Fatalf("expected cleanup to skip all rows, got %#v", repo.rows)
	}
	if repo.lockAttempts != 1 {
		t.Fatalf("expected one lock attempt, got %d", repo.lockAttempts)
	}
	if len(repo.deleteCalls) != 0 {
		t.Fatalf("expected no delete calls, got %#v", repo.deleteCalls)
	}
	if repo.releaseCount != 0 {
		t.Fatalf("expected no release without lock, got %d", repo.releaseCount)
	}
}

// TestSchedulerSnapshotServicePollOutboxDoesNotCleanupOnHandleFailure makes
// last-used publication fail. It checks that watermark, cleanup ownership,
// deletion calls, and the retained event rows remain unchanged after the
// failed handler.
func TestSchedulerSnapshotServicePollOutboxDoesNotCleanupOnHandleFailure(t *testing.T) {
	cache := &outboxCleanupCache{
		updateErr: errors.New("cache update failed"),
	}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{
			{
				ID:        5,
				EventType: SchedulerOutboxEventProviderLastUsed,
				Payload: map[string]any{
					"last_used": map[string]any{"101": float64(123)},
				},
			},
		},
		rows:         []int64{1, 2, 3, 4, 5, 6},
		lockAcquired: true,
	}
	svc := NewSnapshotService(cache, repo, nil, nil, nil)

	svc.pollOutbox()

	if len(cache.setWatermarks) != 0 {
		t.Fatalf("expected no watermark write on handle failure, got %#v", cache.setWatermarks)
	}
	if repo.lockAttempts != 0 {
		t.Fatalf("expected cleanup lock not to be attempted, got %d", repo.lockAttempts)
	}
	if len(repo.deleteCalls) != 0 {
		t.Fatalf("expected no delete calls, got %#v", repo.deleteCalls)
	}
	if !reflect.DeepEqual(repo.rows, []int64{1, 2, 3, 4, 5, 6}) {
		t.Fatalf("expected rows unchanged, got %#v", repo.rows)
	}
}

// TestSchedulerSnapshotServicePollOutboxDoesNotUseConsumedEventForLag consumes
// an hour-old visible event. It checks age lookup starts after its new
// watermark, leaving no degraded count or registry rebuild attempt from that
// consumed event.
func TestSchedulerSnapshotServicePollOutboxDoesNotUseConsumedEventForLag(t *testing.T) {
	cache := &outboxCleanupCache{}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{
			{
				ID:        7,
				EventType: SchedulerOutboxEventProviderLastUsed,
				CreatedAt: time.Now().Add(-time.Hour),
			},
		},
	}
	cfg := &SnapshotOptions{
		OutboxLagWarnSeconds:     1,
		OutboxLagRebuildSeconds:  1,
		OutboxLagRebuildFailures: 1,
	}
	svc := NewSnapshotService(cache, repo, nil, nil, cfg)

	svc.pollOutbox()

	if cache.watermark != 7 {
		t.Fatalf("expected watermark 7, got %d", cache.watermark)
	}
	if !reflect.DeepEqual(repo.firstCreatedAfterID, []int64{7}) {
		t.Fatalf("expected lag check after consumed watermark, got %#v", repo.firstCreatedAfterID)
	}
	if cache.listBucketCalls != 0 {
		t.Fatalf("expected consumed event not to trigger full rebuild, got %d attempts", cache.listBucketCalls)
	}
	if svc.lagFailures != 0 {
		t.Fatalf("expected lag failures to remain reset, got %d", svc.lagFailures)
	}
}

// TestSchedulerSnapshotServiceCheckOutboxLagLatchesPersistentDegradation
// samples persistent age, backlog, or both three times. It checks one registry
// rebuild attempt; the fixture's missing group source makes that rebuild fail,
// so this count also exercises cooldown and does not establish a successful
// latch.
func TestSchedulerSnapshotServiceCheckOutboxLagLatchesPersistentDegradation(t *testing.T) {
	tests := []struct {
		// name identifies the case; createdAt, rows, lagSeconds, and
		// backlogThreshold configure its age and ID-distance observations. The
		// omitted group source makes the full rebuild fail after registry discovery.
		name             string    // degradation observation case label
		createdAt        time.Time // visible pending event timestamp
		rows             []int64   // retained maximum-ID input rows
		lagSeconds       int       // seconds
		backlogThreshold int       // pending ID-distance rebuild threshold
	}{
		{
			name:       "lag",
			createdAt:  time.Now().Add(-time.Hour),
			rows:       []int64{1},
			lagSeconds: 1,
		},
		{
			name:             "backlog",
			createdAt:        time.Now(),
			rows:             []int64{100},
			backlogThreshold: 50,
		},
		{
			name:             "lag_and_backlog",
			createdAt:        time.Now().Add(-time.Hour),
			rows:             []int64{100},
			lagSeconds:       1,
			backlogThreshold: 50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := &outboxCleanupCache{listBuckets: []SchedulerBucket{{Platform: capability.PlatformOpenAI, Mode: SchedulerModeSingle}}}
			repo := &outboxCleanupRepo{
				events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: tt.createdAt}},
				rows:   tt.rows,
			}
			cfg := &SnapshotOptions{
				OutboxLagRebuildSeconds:  tt.lagSeconds,
				OutboxLagRebuildFailures: 1,
				OutboxBacklogRebuildRows: tt.backlogThreshold,
			}
			// The group source is omitted, so discovery cannot successfully complete
			// rebuilding. Registry call counts below protect bounded attempts, not
			// successful-latch state.
			svc := NewSnapshotService(cache, repo, &outboxCleanupProviderRepo{}, nil, cfg)

			for range 3 {
				svc.checkOutboxLag(context.Background(), 0)
			}

			if cache.listBucketCalls != 1 {
				t.Fatalf("expected one rebuild attempt during a persistent degraded episode, got %d", cache.listBucketCalls)
			}
		})
	}
}

// TestSchedulerSnapshotServiceCheckOutboxLagFailedRebuildRearmsAfterRecovery
// first fails registry discovery under persistent age. It checks repeated
// samples stay bounded, then observed recovery and a newly pending old event
// permit another rebuild attempt.
func TestSchedulerSnapshotServiceCheckOutboxLagFailedRebuildRearmsAfterRecovery(t *testing.T) {
	cache := &outboxCleanupCache{listBucketErr: errors.New("list buckets failed")}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now().Add(-time.Hour)}},
		rows:   []int64{1},
	}
	cfg := &SnapshotOptions{
		OutboxLagRebuildSeconds:  1,
		OutboxLagRebuildFailures: 1,
	}
	svc := NewSnapshotService(cache, repo, nil, nil, cfg)

	svc.checkOutboxLag(context.Background(), 0)
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected a failed rebuild to stay bounded within the episode, got %d attempts", cache.listBucketCalls)
	}

	svc.checkOutboxLag(context.Background(), 1)
	repo.events = append(repo.events, SchedulerOutboxEvent{ID: 2, CreatedAt: time.Now().Add(-time.Hour)})
	repo.rows = []int64{2}
	svc.checkOutboxLag(context.Background(), 1)

	if cache.listBucketCalls != 2 {
		t.Fatalf("expected recovery to rearm a failed rebuild for the next episode, got %d attempts", cache.listBucketCalls)
	}
}

// TestSchedulerSnapshotServiceCheckOutboxLagFailedRebuildRetriesAfterCooldownWithoutRecovery
// makes retry times due directly under persistent age. It checks three bounded
// registry attempts; even after clearing the registry error, the missing group
// source still prevents success, so the final call count cannot prove a
// successful latch.
func TestSchedulerSnapshotServiceCheckOutboxLagFailedRebuildRetriesAfterCooldownWithoutRecovery(t *testing.T) {
	cache := &outboxCleanupCache{
		listBucketErr: errors.New("list buckets failed"),
		listBuckets:   []SchedulerBucket{{Platform: capability.PlatformOpenAI, Mode: SchedulerModeSingle}},
	}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now().Add(-time.Hour)}},
		rows:   []int64{1},
	}
	cfg := &SnapshotOptions{
		OutboxLagRebuildSeconds:    1,
		OutboxLagRebuildFailures:   1,
		FullRebuildIntervalSeconds: 0,
	}
	svc := NewSnapshotService(cache, repo, &outboxCleanupProviderRepo{}, nil, cfg)

	svc.checkOutboxLag(context.Background(), 0)
	for range 3 {
		svc.checkOutboxLag(context.Background(), 0)
	}
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected failed rebuild polls to be rate limited, got %d attempts", cache.listBucketCalls)
	}

	svc.lagMu.Lock()
	if !svc.outboxRebuildRetryAt.After(time.Now()) {
		t.Fatal("expected failed rebuild to schedule a future retry")
	}
	// Move the stored retry deadline into the past while holding lagMu. This
	// exercises due-state handling without consuming the production cooldown in
	// test time.
	svc.outboxRebuildRetryAt = time.Now().Add(-time.Second)
	svc.lagMu.Unlock()
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 2 {
		t.Fatalf("expected persistent degradation to retry after cooldown, got %d attempts", cache.listBucketCalls)
	}

	for range 3 {
		svc.checkOutboxLag(context.Background(), 0)
	}
	if cache.listBucketCalls != 2 {
		t.Fatalf("expected repeated rebuild failures to stay rate limited, got %d attempts", cache.listBucketCalls)
	}

	svc.lagMu.Lock()
	// Move the stored retry deadline into the past while holding lagMu. This
	// exercises due-state handling without consuming the production cooldown in
	// test time.
	svc.outboxRebuildRetryAt = time.Now().Add(-time.Second)
	svc.lagMu.Unlock()
	cache.listBucketErr = nil
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 3 {
		t.Fatalf("expected degraded episode to retry after cooldown, got %d attempts", cache.listBucketCalls)
	}

	for range 3 {
		svc.checkOutboxLag(context.Background(), 0)
	}
	if cache.listBucketCalls != 3 {
		t.Fatalf("expected successful retry to latch the degraded episode, got %d attempts", cache.listBucketCalls)
	}
}

// TestSchedulerSnapshotServiceCheckOutboxLagBacklogRetryDoesNotBypassNewLagThreshold
// schedules a failed backlog retry before backlog recovers and age degrades.
// It checks the new age reason must accumulate its own three observations
// before another registry rebuild.
func TestSchedulerSnapshotServiceCheckOutboxLagBacklogRetryDoesNotBypassNewLagThreshold(t *testing.T) {
	cache := &outboxCleanupCache{listBucketErr: errors.New("list buckets failed")}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now()}},
		rows:   []int64{100},
	}
	cfg := &SnapshotOptions{
		OutboxLagRebuildSeconds:  1,
		OutboxLagRebuildFailures: 3,
		OutboxBacklogRebuildRows: 50,
	}
	svc := NewSnapshotService(cache, repo, nil, nil, cfg)

	// The initial backlog condition creates a failed retry generation. Making its
	// recorded deadline due must not let a subsequently different age reason
	// bypass its own observation threshold.
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected the backlog degradation to attempt one rebuild, got %d", cache.listBucketCalls)
	}
	svc.lagMu.Lock()
	// Move the stored retry deadline into the past while holding lagMu. This
	// exercises due-state handling without consuming the production cooldown in
	// test time.
	svc.outboxRebuildRetryAt = time.Now().Add(-time.Second)
	svc.lagMu.Unlock()

	// Use a healthy ID-distance observation with old pending age. Clearing the
	// backlog retry permits a new age count, whose third observation alone should
	// start rebuilding.
	repo.rows = []int64{1}
	repo.events[0].CreatedAt = time.Now().Add(-time.Hour)
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected the new lag episode to start at its own threshold, got %d rebuild attempts", cache.listBucketCalls)
	}

	svc.checkOutboxLag(context.Background(), 0)
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 2 {
		t.Fatalf("expected lag rebuild only after three lag observations, got %d attempts", cache.listBucketCalls)
	}
}

// TestSchedulerSnapshotServiceCheckOutboxLagLagRetryDoesNotDelayOrEscalateNewBacklog
// creates a failed age retry, then recovers age while backlog degrades. It
// checks immediate backlog rebuilding and a fresh failure count of one rather
// than inherited age cooldown or failures.
func TestSchedulerSnapshotServiceCheckOutboxLagLagRetryDoesNotDelayOrEscalateNewBacklog(t *testing.T) {
	cache := &outboxCleanupCache{listBucketErr: errors.New("list buckets failed")}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now().Add(-time.Hour)}},
		rows:   []int64{1},
	}
	cfg := &SnapshotOptions{
		OutboxLagRebuildSeconds:  1,
		OutboxLagRebuildFailures: 1,
		OutboxBacklogRebuildRows: 50,
	}
	svc := NewSnapshotService(cache, repo, nil, nil, cfg)

	// A failed age-triggered registry read leaves an age-specific cooldown.
	// Recovering that age condition later must allow a different backlog reason
	// to start its own failure count.
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected the lag degradation to attempt one rebuild, got %d", cache.listBucketCalls)
	}

	// The pending event's fresh timestamp recovers age while retained ID distance
	// becomes degraded. The new backlog reason should rebuild immediately and
	// restart failed-attempt count at one.
	repo.events[0].CreatedAt = time.Now()
	repo.rows = []int64{100}
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 2 {
		t.Fatalf("expected the new backlog degradation not to inherit lag cooldown, got %d rebuild attempts", cache.listBucketCalls)
	}
	svc.lagMu.Lock()
	failures := svc.outboxRebuildFailures
	svc.lagMu.Unlock()
	if failures != 1 {
		t.Fatalf("expected backlog retry failures to restart at one, got %d", failures)
	}
}

// TestSchedulerSnapshotServiceCheckOutboxLagBacklogRetrySurvivesUnknownBacklog
// makes MaxID unreadable during a failed backlog cooldown. It checks unchanged
// retry reason/count/time, suppressed attempts after observation returns, and
// a new attempt when the recorded due time is moved into the past.
func TestSchedulerSnapshotServiceCheckOutboxLagBacklogRetrySurvivesUnknownBacklog(t *testing.T) {
	cache := &outboxCleanupCache{listBucketErr: errors.New("list buckets failed")}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now()}},
		rows:   []int64{100},
	}
	cfg := &SnapshotOptions{
		OutboxBacklogRebuildRows: 50,
	}
	svc := NewSnapshotService(cache, repo, nil, nil, cfg)

	// A failed registry read reserves a future backlog retry. Later unknown
	// backlog health must preserve that reason and due time rather than treating
	// uncertainty as recovery.
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected one initial backlog rebuild, got %d", cache.listBucketCalls)
	}
	svc.lagMu.Lock()
	retryAt := svc.outboxRebuildRetryAt
	svc.lagMu.Unlock()
	if !retryAt.After(time.Now()) {
		t.Fatalf("expected a future backlog retry, got %s", retryAt)
	}

	// Make the maximum-ID observation fail after backlog recovery was scheduled.
	// Its unknown result must leave retry reason, failure count, and deadline
	// unchanged.
	repo.maxIDErr = errors.New("max id unavailable")
	svc.checkOutboxLag(context.Background(), 0)
	svc.lagMu.Lock()
	retryReason := svc.outboxRebuildRetryReason
	failures := svc.outboxRebuildFailures
	retryAtAfterUnknown := svc.outboxRebuildRetryAt
	svc.lagMu.Unlock()
	if retryReason != "outbox_backlog" || failures != 1 || !retryAtAfterUnknown.Equal(retryAt) {
		t.Fatalf("expected unknown backlog to preserve retry state, got reason=%q failures=%d retry_at=%s", retryReason, failures, retryAtAfterUnknown)
	}

	// Restore maximum-ID observation while keeping its ID distance degraded.
	// Before the stored due time no new registry read should occur; the explicit
	// deadline change below permits another attempt.
	repo.maxIDErr = nil
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected backlog recovery before cooldown to stay rate limited, got %d attempts", cache.listBucketCalls)
	}
	svc.lagMu.Lock()
	// Move the stored retry deadline into the past while holding lagMu. This
	// exercises due-state handling without consuming the production cooldown in
	// test time.
	svc.outboxRebuildRetryAt = time.Now().Add(-time.Second)
	svc.lagMu.Unlock()
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 2 {
		t.Fatalf("expected backlog retry after cooldown expiry, got %d attempts", cache.listBucketCalls)
	}
}

// TestSchedulerSnapshotServiceCheckOutboxLagPreemptsUnknownBacklogRetryAtThreshold
// adds old pending age while a failed backlog retry has unknown health. It
// checks two subthreshold samples do not rebuild, the third preempts, and age
// retry state restarts with one failure and a future due time.
func TestSchedulerSnapshotServiceCheckOutboxLagPreemptsUnknownBacklogRetryAtThreshold(t *testing.T) {
	cache := &outboxCleanupCache{listBucketErr: errors.New("list buckets failed")}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now()}},
		rows:   []int64{100},
	}
	cfg := &SnapshotOptions{
		OutboxLagRebuildSeconds:  1,
		OutboxLagRebuildFailures: 3,
		OutboxBacklogRebuildRows: 50,
	}
	svc := NewSnapshotService(cache, repo, nil, nil, cfg)

	// The first backlog failure establishes a future retry before maximum-ID
	// reads fail. Old pending age can then accumulate its own threshold despite
	// unknown backlog health.
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 1 {
		t.Fatalf("expected one initial backlog rebuild, got %d", cache.listBucketCalls)
	}
	repo.maxIDErr = errors.New("max id unavailable")
	repo.events[0].CreatedAt = time.Now().Add(-time.Hour)

	// Two age observations remain below the configured count while backlog health
	// is unknown. The following third observation must preempt that backlog retry
	// and establish an age-specific failure generation.
	for observation := 1; observation <= 2; observation++ {
		svc.checkOutboxLag(context.Background(), 0)
		if cache.listBucketCalls != 1 {
			t.Fatalf("expected lag observation %d to stay below threshold, got %d rebuild attempts", observation, cache.listBucketCalls)
		}
	}
	svc.checkOutboxLag(context.Background(), 0)
	if cache.listBucketCalls != 2 {
		t.Fatalf("expected lag to preempt backlog cooldown at its threshold, got %d attempts", cache.listBucketCalls)
	}

	svc.lagMu.Lock()
	retryReason := svc.outboxRebuildRetryReason
	failures := svc.outboxRebuildFailures
	retryAt := svc.outboxRebuildRetryAt
	svc.lagMu.Unlock()
	if retryReason != "outbox_lag" || failures != 1 || !retryAt.After(time.Now()) {
		t.Fatalf("expected a fresh lag retry generation, got reason=%q failures=%d retry_at=%s", retryReason, failures, retryAt)
	}
}

// TestOutboxRebuildRetryDelayIsExponentiallyBounded samples delay values for
// twenty consecutive positive failure counts. It checks nondecreasing values,
// the maximum cap, and reaching that cap; it does not assert exact doubling at
// each step.
func TestOutboxRebuildRetryDelayIsExponentiallyBounded(t *testing.T) {
	previous := time.Duration(0)
	// A bounded sequence of positive failure counts reaches the fixed cap;
	// assertions inspect monotonicity and that cap rather than an exact doubling
	// formula.
	for failures := 1; failures <= 20; failures++ {
		delay := outboxRebuildRetryDelay(failures)
		if delay < previous {
			t.Fatalf("expected retry delay to be monotonic, failure %d produced %s after %s", failures, delay, previous)
		}
		if delay > outboxRebuildRetryMaxDelay {
			t.Fatalf("expected retry delay to stay bounded, got %s", delay)
		}
		previous = delay
	}
	if previous != outboxRebuildRetryMaxDelay {
		t.Fatalf("expected repeated failures to reach max delay %s, got %s", outboxRebuildRetryMaxDelay, previous)
	}
}

// TestSchedulerSnapshotServicePollOutboxEmptyBatchClearsDegradedEpisode
// follows a degraded sample with an empty visible poll at the watermark. It
// checks no extra age/backlog health query and a newly pending old event
// rearming another registry rebuild attempt.
func TestSchedulerSnapshotServicePollOutboxEmptyBatchClearsDegradedEpisode(t *testing.T) {
	cache := &outboxCleanupCache{listBuckets: []SchedulerBucket{{Platform: capability.PlatformOpenAI, Mode: SchedulerModeSingle}}}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now().Add(-time.Hour)}},
		rows:   []int64{1},
	}
	cfg := &SnapshotOptions{
		OutboxLagRebuildSeconds:  1,
		OutboxLagRebuildFailures: 1,
		OutboxBacklogRebuildRows: 1,
	}
	svc := NewSnapshotService(cache, repo, &outboxCleanupProviderRepo{}, nil, cfg)

	svc.checkOutboxLag(context.Background(), 0)
	cache.watermark = 1
	svc.pollOutbox()

	if !reflect.DeepEqual(repo.firstCreatedAfterID, []int64{0}) {
		t.Fatalf("expected empty poll to use the empty batch as recovery evidence, got watermarks %#v", repo.firstCreatedAfterID)
	}
	if repo.maxIDCalls != 1 {
		t.Fatalf("expected empty poll to skip a redundant backlog query, got %d health checks", repo.maxIDCalls)
	}

	repo.events = append(repo.events, SchedulerOutboxEvent{ID: 2, CreatedAt: time.Now().Add(-time.Hour)})
	repo.rows = []int64{2}
	svc.checkOutboxLag(context.Background(), 1)
	if cache.listBucketCalls != 2 {
		t.Fatalf("expected empty-poll recovery to rearm the next degraded episode, got %d attempts", cache.listBucketCalls)
	}
}

// TestSchedulerSnapshotServiceOutboxLagWarningIsTransitionLimited calls the
// warning admission helper with repeated active state, recovery, and renewed
// activity. It checks admission only at the initial and renewed false-to-true
// transitions.
func TestSchedulerSnapshotServiceOutboxLagWarningIsTransitionLimited(t *testing.T) {
	svc := NewSnapshotService(nil, nil, nil, nil, nil)

	if !svc.shouldLogOutboxLagWarning(true) {
		t.Fatal("expected the initial degraded transition to log")
	}
	if svc.shouldLogOutboxLagWarning(true) {
		t.Fatal("expected persistent degradation to suppress repeated warnings")
	}
	if svc.shouldLogOutboxLagWarning(true) {
		t.Fatal("expected persistent degradation to suppress repeated warnings")
	}
	if svc.shouldLogOutboxLagWarning(false) {
		t.Fatal("expected recovery not to emit a lag warning")
	}
	if !svc.shouldLogOutboxLagWarning(true) {
		t.Fatal("expected renewed degradation to log after recovery")
	}
}

// TestSchedulerSnapshotServiceCheckOutboxLagSamplesMaxIDErrors calls the
// error-log admission helper with supplied times. It checks initial admission,
// suppression halfway through the interval, and rearming at the full interval
// boundary.
func TestSchedulerSnapshotServiceCheckOutboxLagSamplesMaxIDErrors(t *testing.T) {
	svc := NewSnapshotService(nil, nil, nil, nil, nil)
	now := time.Now()

	if !svc.shouldLogOutboxMaxIDError(now) {
		t.Fatal("expected the first MaxID error to log")
	}
	// A halfway clock sample remains inside the suppression interval; the later
	// full-interval sample checks its inclusive rearm boundary without sleeping.
	if svc.shouldLogOutboxMaxIDError(now.Add(outboxMaxIDErrorLogSampleInterval / 2)) {
		t.Fatal("expected MaxID errors inside the sample interval to be suppressed")
	}
	if !svc.shouldLogOutboxMaxIDError(now.Add(outboxMaxIDErrorLogSampleInterval)) {
		t.Fatal("expected MaxID error logging to rearm after the sample interval")
	}
}

// TestSchedulerSnapshotServicePollOutboxHealthyEmptyBatchSkipsLagHealthQueries
// polls an empty source with both degradation thresholds enabled. It checks
// the empty batch alone requires neither pending-age lookup nor maximum-ID
// lookup.
func TestSchedulerSnapshotServicePollOutboxHealthyEmptyBatchSkipsLagHealthQueries(t *testing.T) {
	cache := &outboxCleanupCache{}
	repo := &outboxCleanupRepo{}
	cfg := &SnapshotOptions{
		OutboxLagRebuildSeconds:  1,
		OutboxLagRebuildFailures: 1,
		OutboxBacklogRebuildRows: 1,
	}
	svc := NewSnapshotService(cache, repo, nil, nil, cfg)

	svc.pollOutbox()

	if len(repo.firstCreatedAfterID) != 0 {
		t.Fatalf("expected healthy empty poll to skip lag query, got watermarks %#v", repo.firstCreatedAfterID)
	}
	if repo.maxIDCalls != 0 {
		t.Fatalf("expected healthy empty poll to skip backlog query, got %d calls", repo.maxIDCalls)
	}
}

// TestSchedulerSnapshotServiceEmptyPollDoesNotReleaseRunningRebuild holds
// registry discovery during a degradation attempt and polls an empty batch. It
// checks a second health sample completes without queuing another generation,
// then releases and joins the first attempt with one discovery total.
func TestSchedulerSnapshotServiceEmptyPollDoesNotReleaseRunningRebuild(t *testing.T) {
	baseCache := &outboxCleanupCache{
		watermark:   1,
		listBuckets: []SchedulerBucket{{Platform: capability.PlatformOpenAI, Mode: SchedulerModeSingle}},
	}
	cache := &blockingOutboxCleanupCache{
		outboxCleanupCache: baseCache,
		started:            make(chan struct{}),
		release:            make(chan struct{}),
	}
	repo := &outboxCleanupRepo{
		events: []SchedulerOutboxEvent{{ID: 1, CreatedAt: time.Now().Add(-time.Hour)}},
		rows:   []int64{1},
	}
	cfg := &SnapshotOptions{
		OutboxLagRebuildSeconds:  1,
		OutboxLagRebuildFailures: 1,
	}
	svc := NewSnapshotService(cache, repo, &outboxCleanupProviderRepo{}, nil, cfg)

	// Block the first registry read after running was reserved. Its started
	// signal orders the empty poll before the first operation can publish
	// completion state.
	firstDone := make(chan struct{})
	go func() {
		svc.checkOutboxLag(context.Background(), 0)
		close(firstDone)
	}()
	select {
	case <-cache.started:
	case <-time.After(time.Second):
		t.Fatal("first rebuild did not start")
	}

	// The empty poll clears observed episode state while the separate registry
	// barrier still holds the active rebuild. A second sample must preserve that
	// running reservation.
	svc.pollOutbox()

	// A second health check must finish while the first registry read remains
	// blocked. The timeout branch releases and joins both checks before reporting
	// failure.
	secondDone := make(chan struct{})
	go func() {
		svc.checkOutboxLag(context.Background(), 0)
		close(secondDone)
	}()
	select {
	case <-secondDone:
	case <-time.After(200 * time.Millisecond):
		close(cache.release)
		<-firstDone
		<-secondDone
		t.Fatal("second lag check queued another rebuild while the first was running")
	}

	close(cache.release)
	<-firstDone
	if calls := cache.listCalls(); calls != 1 {
		t.Fatalf("expected one rebuild generation, got %d", calls)
	}
}

// TestSchedulerSnapshotServiceCleanupSkipsNonPositiveWatermark supplies a zero
// consumed watermark with rows and grantable cleanup ownership. It checks no
// lock or deletion call occurs and the serial fixture rows remain unchanged.
func TestSchedulerSnapshotServiceCleanupSkipsNonPositiveWatermark(t *testing.T) {
	repo := &outboxCleanupRepo{
		rows:         []int64{1, 2, 3},
		lockAcquired: true,
	}
	svc := NewSnapshotService(&outboxCleanupCache{}, repo, nil, nil, nil)

	svc.cleanupConsumedOutbox(0)

	if repo.lockAttempts != 0 {
		t.Fatalf("expected no lock attempt for non-positive watermark, got %d", repo.lockAttempts)
	}
	if len(repo.deleteCalls) != 0 {
		t.Fatalf("expected no delete calls, got %#v", repo.deleteCalls)
	}
	if !reflect.DeepEqual(repo.rows, []int64{1, 2, 3}) {
		t.Fatalf("expected rows unchanged, got %#v", repo.rows)
	}
}

// TestSchedulerFullRebuildCapturesAllRegistryTokensBeforeDBLoad holds the
// first source query after canonical captures. It retires and reopens a queued
// bucket, then checks its old token attempts publication without adopting the
// new epoch while the first bucket publishes.
func TestSchedulerFullRebuildCapturesAllRegistryTokensBeforeDBLoad(t *testing.T) {
	first := SchedulerBucket{GroupID: 61, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	queued := SchedulerBucket{GroupID: 61, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newRetirementRaceCache(first, queued)
	dbStarted := make(chan struct{})
	releaseDB := make(chan struct{})
	// Only the first single-platform read closes the start signal and waits. That
	// barrier exposes queued tokens captured before a later retire/reopen epoch
	// transition.
	var firstDB sync.Once
	repo := &retirementProviderSource{
		listPlatformFunc: func(context.Context, string) ([]SnapshotProvider, error) {
			firstDB.Do(func() {
				close(dbStarted)
				<-releaseDB
			})
			return []SnapshotProvider{snapshotTestProvider{ID: 6101, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}}, nil
		},
	}
	svc := NewSnapshotService(cache, nil, repo, &retirementGroupRepo{groups: []SnapshotGroup{{ID: 61, Status: StatusActive}}}, &SnapshotOptions{DbFallbackEnabled: true})

	result := make(chan error, 1)
	go func() { result <- svc.triggerFullRebuild("retirement_race_a") }()
	select {
	case <-dbStarted:
	case <-time.After(time.Second):
		t.Fatal("first DB load did not start")
	}

	captures, reopens := cache.captureAndReopenCounts()
	require.Equal(t, len(schedulerCanonicalBuckets(0))*2, captures, "group0 and active-group canonical tokens must be captured before the first DB load")
	require.Zero(t, reopens)
	require.NoError(t, cache.RetireBucket(context.Background(), queued))
	_, err := cache.ReopenBucket(context.Background(), queued)
	require.NoError(t, err)
	// The queued bucket's epoch changed while provider work was held. Resume and
	// join rebuilding before checking that its original token did not adopt the
	// new epoch.
	close(releaseDB)
	require.NoError(t, <-result)

	_, firstPublished := cache.counts(first)
	queuedAttempts, queuedPublished := cache.counts(queued)
	require.Equal(t, 1, firstPublished)
	require.Equal(t, 1, queuedAttempts)
	require.Zero(t, queuedPublished, "queued registry task must not adopt the reopened epoch")
}

// TestSchedulerRebuildRetireAfterDBLoadFencesPublish pauses fixture
// publication after its provider query returned. It retires the bucket before
// the fixture's fenced version increment, then checks ordinary completion
// skips publication and leaves version zero.
func TestSchedulerRebuildRetireAfterDBLoadFencesPublish(t *testing.T) {
	bucket := SchedulerBucket{GroupID: 62, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	cache := newRetirementRaceCache()
	dbReturned := make(chan struct{})
	setEntered := make(chan struct{})
	releaseSet := make(chan struct{})
	// Hold the writer before its guarded version increment, after the source
	// returned. Retirement can then be observed at that exact publication
	// boundary.
	cache.beforeSet = func() {
		close(setEntered)
		<-releaseSet
	}
	repo := &retirementProviderSource{
		listPlatformFunc: func(context.Context, string) ([]SnapshotProvider, error) {
			close(dbReturned)
			return []SnapshotProvider{snapshotTestProvider{ID: 6201, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}}, nil
		},
	}
	svc := NewSnapshotService(cache, nil, repo, nil, &SnapshotOptions{DbFallbackEnabled: true})

	result := make(chan error, 1)
	go func() {
		result <- svc.rebuildBuckets(context.Background(), []SchedulerBucket{bucket}, "retirement_race_b")
	}()
	select {
	case <-dbReturned:
	case <-time.After(time.Second):
		t.Fatal("DB load did not return")
	}
	select {
	case <-setEntered:
	case <-time.After(time.Second):
		t.Fatal("snapshot writer did not reach allocation boundary")
	}
	require.NoError(t, cache.RetireBucket(context.Background(), bucket))
	// Retirement completed before allowing the guarded write to continue. Join
	// the rebuild result before inspecting attempt, publication, and version
	// observations.
	close(releaseSet)
	require.NoError(t, <-result)

	setAttempts, published := cache.counts(bucket)
	require.Equal(t, 1, setAttempts)
	require.Zero(t, published)
	require.Zero(t, cache.version(bucket), "retirement before allocation must not advance the snapshot version")
}

// TestSchedulerFallbackReturnsDBProvidersWhenBucketRetired starts with a
// retired group bucket and permitted recovery. It checks database providers
// still return with useMixed=false and no cache publication attempt or
// accepted write.
func TestSchedulerFallbackReturnsDBProvidersWhenBucketRetired(t *testing.T) {
	bucket := SchedulerBucket{GroupID: 63, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	cache := newRetirementRaceCache()
	require.NoError(t, cache.RetireBucket(context.Background(), bucket))
	repo := &retirementProviderSource{
		providers: []SnapshotProvider{snapshotTestProvider{ID: 6301, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}},
	}
	svc := NewSnapshotService(cache, nil, repo, nil, &SnapshotOptions{DbFallbackEnabled: true})
	groupID := bucket.GroupID

	providers, useMixed, err := svc.ListSchedulableProviders(context.Background(), &groupID, bucket.Platform, false)
	require.NoError(t, err)
	require.False(t, useMixed)
	require.Len(t, providers, 1)
	setAttempts, published := cache.counts(bucket)
	require.Zero(t, setAttempts)
	require.Zero(t, published)
}

// TestSchedulerSnapshotService_ListSchedulableProvidersStopsWhenCacheContextCanceled
// passes an already-canceled context and nil group. It checks entry
// cancellation, nil providers, useMixed=false, and zero source reads; this
// setup returns before cache.GetSnapshot and does not exercise cache-returned
// cancellation.
func TestSchedulerSnapshotService_ListSchedulableProvidersStopsWhenCacheContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// Cancellation is established before entering selection with a nil group. The
	// entry context check returns before cache.GetSnapshot, so the test observes
	// no database fallback.
	cancel()
	repo := &schedulerSnapshotFallbackRepoStub{}
	svc := NewSnapshotService(schedulerSnapshotContextCacheStub{}, nil, repo, nil, nil)

	providers, useMixed, err := svc.ListSchedulableProviders(ctx, nil, capability.PlatformOpenAI, false)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, providers)
	require.False(t, useMixed)
	require.Equal(t, 0, repo.calls)
}

// TestSchedulerSnapshotPlatformsIncludesQoder reads the canonical platform
// list used for snapshot enumeration. It checks that Qoder remains present so
// canonical rebuilding can include that platform.
func TestSchedulerSnapshotPlatformsIncludesQoder(t *testing.T) {
	require.Contains(t, schedulerSnapshotPlatforms(), capability.PlatformQoder)
}

// TestSchedulerSnapshotServiceCanonicalBucketsIncludesQoder enumerates
// canonical buckets for group zero. It checks Qoder has both single and forced
// identities while a Qoder mixed-mode identity is absent.
func TestSchedulerSnapshotServiceCanonicalBucketsIncludesQoder(t *testing.T) {
	buckets := schedulerCanonicalBuckets(0)

	require.Contains(t, buckets, SchedulerBucket{GroupID: 0, Platform: capability.PlatformQoder, Mode: SchedulerModeSingle})
	require.Contains(t, buckets, SchedulerBucket{GroupID: 0, Platform: capability.PlatformQoder, Mode: SchedulerModeForced})
	require.NotContains(t, buckets, SchedulerBucket{GroupID: 0, Platform: capability.PlatformQoder, Mode: SchedulerModeMixed})
}

// TestSnapshotStartAfterStop stops a service before attempting worker startup
// and stopping again. It checks that the fixture receives no initial
// registry-read notification after permanent runtime shutdown.
func TestSnapshotStartAfterStop(t *testing.T) {
	c := &planSnapshotCache{calls: make(chan struct{}, 4)}
	s := NewSnapshotService(c, nil, nil, nil, nil)
	s.Stop()
	s.Start()
	s.Stop()
	if len(c.calls) > 0 {
		t.Fatalf("停止后仍执行初始重建: calls=%d", len(c.calls))
	}
}

// TestSnapshotRepeatedStart observes the first initial registry read before
// calling Start again and stopping. It checks that the second Start leaves no
// additional read notification in the buffered fixture channel.
func TestSnapshotRepeatedStart(t *testing.T) {
	c := &planSnapshotCache{calls: make(chan struct{}, 4)}
	s := NewSnapshotService(c, nil, nil, nil, nil)
	s.Start()
	// Observe the first registered startup task before repeating Start. Stop then
	// joins worker completion before the buffered notification count is
	// inspected.
	<-c.calls
	s.Start()
	s.Stop()
	if len(c.calls) > 0 {
		t.Fatal("重复 Start 再次执行初始重建")
	}
}

// TestSnapshotStopCancelsWork holds startup registry discovery at a
// context-aware barrier. It checks Stop completes within the observation
// budget, then opens the barrier and joins shutdown cleanup even after a
// failed timing assertion.
func TestSnapshotStopCancelsWork(t *testing.T) {
	c := &planSnapshotCache{calls: make(chan struct{}, 4), block: make(chan struct{})}
	s := NewSnapshotService(c, nil, nil, nil, nil)
	s.Start()
	<-c.calls
	// Run shutdown while startup discovery is held at its context-aware barrier.
	// The completion signal tests cancellation rather than an unblocked planned
	// source error.
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Error("Stop 没有取消支持 context 的在途重建")
	}
	// Open the fixture barrier after the timing assertion and join shutdown.
	// Cleanup still finishes when the assertion reports an error rather than
	// aborting the test.
	close(c.block)
	<-done
}

// TestSchedulerSnapshotService_UpdateProviderInCache covers immediate provider
// forwarding, absent cache/provider inputs, and configured publication errors.
// It checks the observed provider identity, no write for nil input, and
// preserved error cause.
func TestSchedulerSnapshotService_UpdateProviderInCache(t *testing.T) {
	t.Run("calls cache.SetProvider", func(t *testing.T) {
		cache := &updateSnapshotCache{}
		core := NewSnapshotService(cache, nil, nil, nil, nil)
		require.NoError(t, core.UpdateProviderInCache(t.Context(), updateSnapshotValue{123}))
		require.Len(t, cache.values, 1)
		require.Equal(t, int64(123), cache.values[0].SnapshotMetadata().ID)
	})
	t.Run("returns nil when cache is nil", func(t *testing.T) {
		core := NewSnapshotService(nil, nil, nil, nil, nil)
		require.NoError(t, core.UpdateProviderInCache(t.Context(), updateSnapshotValue{1}))
	})
	t.Run("returns nil when provider is nil", func(t *testing.T) {
		cache := &updateSnapshotCache{}
		core := NewSnapshotService(cache, nil, nil, nil, nil)
		require.NoError(t, core.UpdateProviderInCache(t.Context(), nil))
		require.Empty(t, cache.values)
	})
	t.Run("propagates cache error", func(t *testing.T) {
		expected := errors.New("cache error")
		core := NewSnapshotService(&updateSnapshotCache{err: expected}, nil, nil, nil, nil)
		require.ErrorIs(t, core.UpdateProviderInCache(t.Context(), updateSnapshotValue{1}), expected)
	})
}

// newBatchProviderQueryRepo allocates call and scripted-result maps for source
// query tests. The embedded identity readers remain nil, and tests configure
// scripts and hooks before asynchronous service calls use them.
func newBatchProviderQueryRepo() *batchProviderQueryRepo {
	return &batchProviderQueryRepo{
		calls:   make(map[batchProviderQueryKey]int),
		results: make(map[batchProviderQueryKey][]batchProviderQueryResult),
	}
}

// ListSchedulableByGroupIDAndPlatform records a grouped single-platform query
// through run using both supplied identities. The fixture ignores context
// cancellation; script selection depends on the key and its recorded call
// ordinal.
func (r *batchProviderQueryRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]SnapshotProvider, error) {
	return r.run(batchProviderQueryKey{groupID: groupID, platform: platform})
}

// ListSchedulableByGroupIDAndPlatforms records a grouped plural-reader key
// using the first supplied platform and mixed=true. Callers provide a nonempty
// platform slice; remaining entries and context cancellation do not change
// this fixture result.
func (r *batchProviderQueryRepo) ListSchedulableByGroupIDAndPlatforms(_ context.Context, groupID int64, platforms []string) ([]SnapshotProvider, error) {
	return r.run(batchProviderQueryKey{groupID: groupID, platform: platforms[0], mixed: true})
}

// ListSchedulableUngroupedByPlatform records a group-zero single-platform
// source key for fixture assertions. It delegates scripts and synthesized data
// to run and ignores the supplied context's cancellation state.
func (r *batchProviderQueryRepo) ListSchedulableUngroupedByPlatform(_ context.Context, platform string) ([]SnapshotProvider, error) {
	return r.run(batchProviderQueryKey{platform: platform})
}

// ListSchedulableUngroupedByPlatforms records group zero, mixed=true, and the
// first platform for scripted query selection. Callers supply a nonempty
// platform slice. Remaining platforms are ignored, and the context is not
// passed to run or its observation hook.
func (r *batchProviderQueryRepo) ListSchedulableUngroupedByPlatforms(_ context.Context, platforms []string) ([]SnapshotProvider, error) {
	return r.run(batchProviderQueryKey{platform: platforms[0], mixed: true})
}

// ListModelAvailabilityCandidates panics when a snapshot test reaches
// model-availability discovery. The fixture's intended scheduler rebuild paths
// use its schedulable readers, so this method exposes an unexpected source
// call immediately.
func (r *batchProviderQueryRepo) ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]SnapshotProvider, error) {
	panic("unexpected ListModelAvailabilityCandidates call")
}

// ListSchedulableByPlatform records the supplied platform under a group-zero
// single-reader key. It delegates call-dependent source results to run, making
// ungrouped fallback calls observable even though their context is ignored.
func (r *batchProviderQueryRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]SnapshotProvider, error) {
	return r.run(batchProviderQueryKey{platform: platform})
}

// ListSchedulableByPlatforms records the first platform under a group-zero
// plural-reader key. The input must be nonempty; the scripted fixture
// distinguishes this reader with mixed=true while retaining no other platform
// identities.
func (r *batchProviderQueryRepo) ListSchedulableByPlatforms(_ context.Context, platforms []string) ([]SnapshotProvider, error) {
	return r.run(batchProviderQueryKey{platform: platforms[0], mixed: true})
}

// run records a per-key call ordinal and captures the scripted result slice
// and hook under the query mutex. It unlocks and executes the hook before
// indexing that captured slice for the recorded call. Tests keep scripts
// stable during concurrent reads.
//
// A selected scripted result copies provider slice entries while retaining
// their objects and configured error; an empty source slice becomes nil
// through append. Calls beyond the script synthesize one provider whose ID is
// the call ordinal and platform matches the key. The hook still runs for
// failed scripted reads, allowing preparation assertions before result
// selection without holding the source mutex.
func (r *batchProviderQueryRepo) run(key batchProviderQueryKey) ([]SnapshotProvider, error) {
	// Capture the call ordinal with its script and hook under one lock. Invoke
	// the hook after unlocking so cache observations can be made without
	// retaining the source lock.
	r.mu.Lock()
	r.calls[key]++
	call := r.calls[key]
	results := r.results[key]
	beforeRun := r.beforeRun
	r.mu.Unlock()

	if beforeRun != nil {
		beforeRun(key)
	}
	if call <= len(results) {
		result := results[call-1]
		return append([]SnapshotProvider(nil), result.providers...), result.err
	}
	return []SnapshotProvider{snapshotTestProvider{
		ID:          int64(call),
		Name:        "source",
		Platform:    key.platform,
		Status:      StatusActive,
		Schedulable: true,
	}}, nil
}

// callCount returns the source query key's recorded count under its mutex.
func (r *batchProviderQueryRepo) callCount(key batchProviderQueryKey) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[key]
}

// newBatchSnapshotProviderIDCache constructs the base cache plus full/ID-only
// counters and failure maps. Its writers can model errors before or after
// publication; tests install those errors before the service uses them.
func newBatchSnapshotProviderIDCache() *batchSnapshotProviderIDCache {
	return &batchSnapshotProviderIDCache{
		batchSnapshotCache: newBatchSnapshotCache(),
		fullCalls:          make(map[SchedulerBucket]int),
		idOnlyCalls:        make(map[SchedulerBucket]int),
		idOnlyError:        make(map[SchedulerBucket]error),
		fullLateErr:        make(map[SchedulerBucket]error),
	}
}

// SetSnapshotAndReturnProviderIDs counts a full-writer attempt before
// delegating base publication. A base failure returns no IDs, while
// fullLateErr can fail after that publication succeeded. Tests configure these
// faults before asynchronous access.
//
// A successful returnEmpty mode produces a nonnil empty slice. Otherwise the
// method derives fixture IDs in provider order, accepting only
// snapshotTestData's supported representations. These IDs model the writer
// result consumed by the service; this fixture does not perform provider
// encoding and cannot establish codec omission behavior itself.
func (c *batchSnapshotProviderIDCache) SetSnapshotAndReturnProviderIDs(ctx context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, providers []SnapshotProvider) ([]int64, error) {
	c.reuseMu.Lock()
	// Count the chosen full-writer path before base publication. Failed writes
	// still retain this path observation, letting the next bucket's behavior be
	// checked independently.
	c.fullCalls[bucket]++
	c.reuseMu.Unlock()
	if err := c.SetSnapshot(ctx, bucket, token, providers); err != nil {
		return nil, err
	}
	c.reuseMu.Lock()
	lateErr := c.fullLateErr[bucket]
	returnEmpty := c.returnEmpty
	c.reuseMu.Unlock()
	// The base write already succeeded before this injected late error. Returning
	// no IDs keeps that incomplete writer result distinct from a successful
	// reusable encoding result.
	if lateErr != nil {
		return nil, lateErr
	}
	if returnEmpty {
		return []int64{}, nil
	}
	ids := make([]int64, 0, len(providers))
	for _, provider := range providers {
		ids = append(ids, snapshotTestData(provider).ID)
	}
	return ids, nil
}

// SetSnapshotByProviderIDs counts an ID-only attempt, applies its configured
// error, and delegates publication of reconstructed ID fixtures. The method
// allocates new slice entries containing only identity data.
//
// Base token validation still determines whether this bucket can publish. An
// injected ID-only error happens before that base write, making error
// propagation and attempted writer path independently observable. The
// fixture's reconstructed providers model membership arguments; it does not
// rewrite Redis payloads or simulate the production encoder's retained data.
func (c *batchSnapshotProviderIDCache) SetSnapshotByProviderIDs(ctx context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, providerIDs []int64) error {
	c.reuseMu.Lock()
	c.idOnlyCalls[bucket]++
	err := c.idOnlyError[bucket]
	c.reuseMu.Unlock()
	if err != nil {
		return err
	}
	// Reconstruct identity-only fixtures for the base writer's recorded
	// membership arguments. This models the selected writer path; production
	// ID-only publication does not reencode provider payloads.
	providers := make([]SnapshotProvider, 0, len(providerIDs))
	for _, id := range providerIDs {
		providers = append(providers, snapshotTestProvider{ID: id})
	}
	return c.SetSnapshot(ctx, bucket, token, providers)
}

// reuseCounts returns the full and ID-only writer counts for one bucket.
func (c *batchSnapshotProviderIDCache) reuseCounts(bucket SchedulerBucket) (full, idOnly int) {
	c.reuseMu.Lock()
	defer c.reuseMu.Unlock()
	return c.fullCalls[bucket], c.idOnlyCalls[bucket]
}

// newBatchSnapshotCache allocates capture, lock, fault, write, and version
// maps for rebuilding tests. Defaults permit local publication; tests use the
// overridden rebuild methods and configure failures before concurrent use.
func newBatchSnapshotCache() *batchSnapshotCache {
	return &batchSnapshotCache{
		captured:    make(map[SchedulerBucket]SchedulerBucketWriteToken),
		locks:       make(map[SchedulerBucket]int),
		lockBusy:    make(map[SchedulerBucket]bool),
		lockErrors:  make(map[SchedulerBucket]error),
		setErrors:   make(map[SchedulerBucket]error),
		setAttempts: make(map[SchedulerBucket]int),
		writes:      make(map[SchedulerBucket][]batchSnapshotWrite),
		versions:    make(map[SchedulerBucket]int),
	}
}

// CaptureBucketWriteToken records capture and allocates a new fixture-wide
// epoch under mu. It stores that token as the latest accepted value for this
// bucket, so repeated captures can fence earlier fixture writes.
//
// Each bucket still receives its own identity in the token, and the capture
// list preserves call order for preparation assertions. This synthetic
// epoch-per-capture scheme distinguishes batch writes but does not model the
// storage adapter's real lifecycle epoch allocation. The fixture ignores
// context cancellation and has no retirement path.
func (c *batchSnapshotCache) CaptureBucketWriteToken(_ context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Allocate a distinct fixture capture ordinal while recording its bucket
	// association. Later writes compare the stored latest token, making earlier
	// same-bucket captures rejectable.
	c.nextEpoch++
	token := SchedulerBucketWriteToken{Bucket: bucket, Epoch: c.nextEpoch}
	c.captures = append(c.captures, bucket)
	c.captured[bucket] = token
	return token, nil
}

// TryLockBucket counts each acquisition under mu, returning a configured error
// before evaluating contention. Otherwise lockBusy controls success; the
// fixture records attempts without maintaining a held bucket owner or honoring
// its TTL.
func (c *batchSnapshotCache) TryLockBucket(_ context.Context, bucket SchedulerBucket, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Record acquisition attempts before injected errors or busy status.
	// Assertions can therefore distinguish attempted locking from any following
	// source read or publication.
	c.locks[bucket]++
	if err := c.lockErrors[bucket]; err != nil {
		return false, err
	}
	return !c.lockBusy[bucket], nil
}

// UnlockBucket reports successful batch-fixture cleanup without changing
// recorded bucket state.
func (c *batchSnapshotCache) UnlockBucket(context.Context, SchedulerBucket) error {
	return nil
}

// SetSnapshot records publication attempts and accepts only the latest
// captured token matching this bucket. beforeSet executes before the mutex so
// tests can inspect batch data or interleave other fixture work. Configured
// setErrors are checked after token validity.
//
// A successful write increments this bucket's version and stores a copied
// provider slice with its token. Nested provider objects remain shared, so
// tests keep them stable while assertions read recorded writes. Failed
// validity or injected errors retain the attempt count but create no version
// or write record; context cancellation is not modeled by this cache.
func (c *batchSnapshotCache) SetSnapshot(_ context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, providers []SnapshotProvider) error {
	// Run the configured observation before acquiring the write mutex. Tests can
	// inspect resident query-cache entries at this boundary without a held cache
	// observation lock.
	if c.beforeSet != nil {
		c.beforeSet()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Count the attempted write before fencing and injected errors. Only accepted
	// publication creates its version and record, preserving the failed-attempt
	// distinction for assertions.
	c.setAttempts[bucket]++
	if token != c.captured[bucket] || !token.ValidFor(bucket) {
		return ErrSchedulerBucketWriteFenced
	}
	if err := c.setErrors[bucket]; err != nil {
		return err
	}
	c.versions[bucket]++
	c.writes[bucket] = append(c.writes[bucket], batchSnapshotWrite{
		token:     token,
		providers: append([]SnapshotProvider(nil), providers...),
	})
	return nil
}

// captureCount returns the number of batch-fixture capture observations under
// mu.
func (c *batchSnapshotCache) captureCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.captures)
}

// bucketState samples counts and copies the outer write-record slice under mu
// for one bucket. Nested providers slices and objects remain shared, so
// callers read them while fixture publication data stays stable.
func (c *batchSnapshotCache) bucketState(bucket SchedulerBucket) (locks, attempts, version int, writes []batchSnapshotWrite) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.locks[bucket], c.setAttempts[bucket], c.versions[bucket], append([]batchSnapshotWrite(nil), c.writes[bucket]...)
}

// newBatchQueryTestService binds the supplied cache/provider fixtures with
// empty snapshot options. It supplies no outbox or group source and is
// intended for direct prepared/bucket rebuild calls rather than full lifecycle
// discovery.
func newBatchQueryTestService(cache SnapshotCache, providers SnapshotProviderSource) *SnapshotService {
	return NewSnapshotService(cache, nil, providers, nil, &SnapshotOptions{})
}

// ListSchedulableByGroupIDAndPlatform returns the benchmark's borrowed
// provider slice directly and no error. Group, platform, and cancellation
// inputs are ignored; the count-only benchmark writer permits nil provider
// entries in this workload.
func (r *batchQueryBenchmarkRepo) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]SnapshotProvider, error) {
	return r.providers, nil
}

// CaptureBucketWriteToken supplies the requested bucket with constant fixture
// epoch one. Repeated calls reuse that epoch, allowing the benchmark to
// measure local query-cache machinery rather than lifecycle storage
// coordination.
func (c *batchQueryBenchmarkCache) CaptureBucketWriteToken(_ context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}

// TryLockBucket grants every count-only benchmark acquisition with no error.
// Bucket, lifetime, and context inputs are ignored, and simultaneous calls
// create no retained owner state; the benchmark measures local lease/query
// work.
func (c *batchQueryBenchmarkCache) TryLockBucket(context.Context, SchedulerBucket, time.Duration) (bool, error) {
	return true, nil
}

// UnlockBucket reports successful local release for the count-only benchmark.
func (c *batchQueryBenchmarkCache) UnlockBucket(context.Context, SchedulerBucket) error {
	return nil
}

// SetSnapshot stores the provider slice length in the benchmark sink and
// succeeds. It does not inspect provider metadata, tokens, or context, so
// benchmark entries may remain nil.
func (c *batchQueryBenchmarkCache) SetSnapshot(_ context.Context, _ SchedulerBucket, _ SchedulerBucketWriteToken, providers []SnapshotProvider) error {
	batchQueryBenchmarkProviderCount = len(providers)
	return nil
}

// AcquireBucketLease wraps an accepted batch-fixture lock in BucketLease
// cleanup. Contention and errors return without a lease; successful callers
// release the handle so its callback invokes this fixture's UnlockBucket.
func (c *batchSnapshotCache) AcquireBucketLease(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (*BucketLease, bool, error) {
	ok, err := c.TryLockBucket(ctx, bucket, ttl)
	if err != nil || !ok {
		return nil, ok, err
	}
	return NewBucketLease(func(cleanup context.Context) error { return c.UnlockBucket(cleanup, bucket) }), true, nil
}

// AcquireBucketLease wraps benchmark lock success in a releasable BucketLease
// callback. The callback delegates local UnlockBucket, adding lease
// construction/cleanup work to each benchmarked bucket attempt without a
// storage owner lock.
func (c *batchQueryBenchmarkCache) AcquireBucketLease(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (*BucketLease, bool, error) {
	ok, err := c.TryLockBucket(ctx, bucket, ttl)
	if err != nil || !ok {
		return nil, ok, err
	}
	return NewBucketLease(func(cleanup context.Context) error { return c.UnlockBucket(cleanup, bucket) }), true, nil
}

// newBulkEventProviderRepo retains the configured bulk provider objects and
// creates inherited query scripts. GetByIDs returns that complete fixture
// list; subsequent group/platform rebuild queries use the embedded batch
// source.
func newBulkEventProviderRepo(providers ...SnapshotProvider) *bulkEventProviderRepo {
	return &bulkEventProviderRepo{
		batchProviderQueryRepo: newBatchProviderQueryRepo(),
		providers:              providers,
	}
}

// GetByIDs returns a slice copy of all configured bulk records, irrespective
// of requested IDs. Provider objects remain borrowed, letting event tests
// describe existing records and infer missing requested IDs separately.
func (r *bulkEventProviderRepo) GetByIDs(context.Context, []int64) ([]SnapshotProvider, error) {
	return append([]SnapshotProvider(nil), r.providers...), nil
}

// newBulkEventSnapshotCache constructs immediate-update observation around a
// fresh batch bucket cache. Provider ID logs start empty and use their own
// mutex, while inherited token and publication observations retain the base
// rules.
func newBulkEventSnapshotCache() *bulkEventSnapshotCache {
	return &bulkEventSnapshotCache{batchSnapshotCache: newBatchSnapshotCache()}
}

// SetProvider records the fixture provider's ID under providerMu and returns
// nil. Inputs must be snapshotTestProvider values or usable nonnil
// *snapshotTestProvider pointers; snapshotTestData panics for other dynamic
// types, and a nil pointer cannot supply ID.
func (c *bulkEventSnapshotCache) SetProvider(_ context.Context, provider SnapshotProvider) error {
	// Record immediate payload updates under their own observation mutex. Bucket
	// token/write observations remain separate so bulk ordering can be asserted
	// after handling completes.
	c.providerMu.Lock()
	defer c.providerMu.Unlock()
	c.setProviderIDs = append(c.setProviderIDs, snapshotTestData(provider).ID)
	return nil
}

// DeleteProvider records the requested provider deletion ID under its
// observation mutex.
func (c *bulkEventSnapshotCache) DeleteProvider(_ context.Context, providerID int64) error {
	// Keep requested deletions in the immediate-provider log independently of
	// bucket publication. Missing-record cases can then verify both deletion and
	// the broadened captured rebuild set.
	c.providerMu.Lock()
	defer c.providerMu.Unlock()
	c.deleteProviderIDs = append(c.deleteProviderIDs, providerID)
	return nil
}

// providerWrites returns independently copied set/deletion ID slices under
// providerMu. Empty logs remain nil, and callers can modify the returned slice
// storage without changing the fixture's recorded IDs.
func (c *bulkEventSnapshotCache) providerWrites() (set []int64, deleted []int64) {
	c.providerMu.Lock()
	defer c.providerMu.Unlock()
	return append([]int64(nil), c.setProviderIDs...), append([]int64(nil), c.deleteProviderIDs...)
}

// capturedBuckets returns a copied inherited capture slice under its mutex. An
// empty log returns nil; SchedulerBucket values contain scalar/string
// identities, so callers can modify the returned entries independently.
func (c *bulkEventSnapshotCache) capturedBuckets() []SchedulerBucket {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]SchedulerBucket(nil), c.captures...)
}

// newBulkEventTestService binds bulk cache/source fixtures with empty options
// and no group/outbox source. Tests invoke bulk handling directly, allowing
// immediate provider mutations and prepared bucket captures to be inspected
// separately.
func newBulkEventTestService(cache SnapshotCache, providers SnapshotProviderSource) *SnapshotService {
	return NewSnapshotService(cache, nil, providers, nil, &SnapshotOptions{})
}

// bulkEventPayload creates nonnil []any slices for provider IDs and former
// group IDs under their separate payload keys. Empty inputs produce nonnil
// empty arrays in that representation. Numeric entries retain their int64
// values.
//
// Input order, duplicates, and nonpositive values remain present for the
// handler's parser and deduplication rules. Each array copies integer values,
// so changing an input slice later does not change the supplied payload IDs.
// This helper models the decoded array shape accepted by the service; it does
// not perform a JSON encode/decode round trip.
func bulkEventPayload(providerIDs []int64, groupIDs []int64) map[string]any {
	// The handler accepts decoded JSON array shape []any. Convert both ID lists
	// into that representation so fixture values reach the same numeric parser as
	// visible outbox payloads.
	providerValues := make([]any, 0, len(providerIDs))
	for _, id := range providerIDs {
		providerValues = append(providerValues, id)
	}
	groupValues := make([]any, 0, len(groupIDs))
	for _, id := range groupIDs {
		groupValues = append(groupValues, id)
	}
	return map[string]any{
		"provider_ids": providerValues,
		"group_ids":    groupValues,
	}
}

// schedulerBucketsForTest enumerates single/forced identities for supplied
// groups and platforms, adding the shared empty platform when absent. It
// preserves input ordering and does not deduplicate groups or nonempty
// platforms.
func schedulerBucketsForTest(groupIDs []int64, platforms ...string) []SchedulerBucket {
	if !slices.Contains(platforms, "") {
		platforms = append([]string{""}, platforms...)
	}
	buckets := make([]SchedulerBucket, 0, len(groupIDs)*len(platforms)*2)
	for _, platform := range platforms {
		for _, groupID := range groupIDs {
			buckets = append(buckets,
				SchedulerBucket{GroupID: groupID, Platform: platform, Mode: SchedulerModeSingle},
				SchedulerBucket{GroupID: groupID, Platform: platform, Mode: SchedulerModeForced},
			)
		}
	}
	return buckets
}

// newFullRebuildLifecycleCache adds capture-error and one-shot contention maps
// to an authority/epoch fixture. The supplied registry identities stay
// retained by the nested race cache; tests configure discovery faults before
// concurrent work.
func newFullRebuildLifecycleCache(buckets ...SchedulerBucket) *fullRebuildLifecycleCache {
	return &fullRebuildLifecycleCache{
		groupLifecycleTestCache: newGroupLifecycleTestCache(buckets...),
		captureErrors:           make(map[string]error),
		lockBusyOnce:            make(map[string]bool),
	}
}

// CaptureBucketWriteToken records every capture attempt and checks its
// configured bucket-string error under the outer mutex. Successful attempts
// delegate epoch validation to retirementRaceCache, whose own capture list
// therefore omits injected outer failures.
func (c *fullRebuildLifecycleCache) CaptureBucketWriteToken(ctx context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	c.mu.Lock()
	// Record attempts before injected capture failure or nested epoch checks. The
	// full-rebuild assertions must see preparation work even when no token was
	// returned.
	c.captureAttempts = append(c.captureAttempts, bucket)
	err := c.captureErrors[bucket.String()]
	c.mu.Unlock()
	if err != nil {
		return SchedulerBucketWriteToken{}, err
	}
	return c.retirementRaceCache.CaptureBucketWriteToken(ctx, bucket)
}

// ListBuckets delegates the configured registry read, then filters retired
// identities under the epoch mutex. It returns new slice storage, allowing
// second-round discovery to omit groups retired by the earlier fixture run.
func (c *fullRebuildLifecycleCache) ListBuckets(ctx context.Context) ([]SchedulerBucket, error) {
	buckets, err := c.groupLifecycleTestCache.ListBuckets(ctx)
	if err != nil {
		return nil, err
	}
	c.retirementRaceCache.mu.Lock()
	defer c.retirementRaceCache.mu.Unlock()
	registered := make([]SchedulerBucket, 0, len(buckets))
	for _, bucket := range buckets {
		if !c.retired[bucket.String()] {
			registered = append(registered, bucket)
		}
	}
	return registered, nil
}

// TryLockBucket consumes a configured one-shot busy flag under the outer
// mutex. A busy call returns before delegated lock observations; later calls
// use the group-lifecycle fixture's TTL/error recording and success policy.
func (c *fullRebuildLifecycleCache) TryLockBucket(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	busy := c.lockBusyOnce[bucket.String()]
	if busy {
		// Consume the one-shot contention flag before returning the skipped lock. A
		// later poll can acquire the same bucket and demonstrate retry without
		// changing group authority.
		delete(c.lockBusyOnce, bucket.String())
	}
	c.mu.Unlock()
	if busy {
		return false, nil
	}
	return c.groupLifecycleTestCache.TryLockBucket(ctx, bucket, ttl)
}

// GetOutboxWatermark returns the full-rebuild fixture's consumption ID under
// its mutex.
func (c *fullRebuildLifecycleCache) GetOutboxWatermark(context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.watermark, nil
}

// SetOutboxWatermark records the supplied consumption ID and its publication
// order.
func (c *fullRebuildLifecycleCache) SetOutboxWatermark(_ context.Context, id int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Assign the consumption ID and append its observation under the same mutex.
	// Tests inspect completion only after all event handlers in the polled batch
	// returned.
	c.watermark = id
	c.watermarkWrites = append(c.watermarkWrites, id)
	return nil
}

// captureAttemptCount returns all outer capture attempts, including injected
// failures.
func (c *fullRebuildLifecycleCache) captureAttemptCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.captureAttempts)
}

// currentWatermark returns the latest fixture consumption ID under the outer
// mutex.
func (c *fullRebuildLifecycleCache) currentWatermark() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.watermark
}

// totalSetAttempts sums publication observations while holding the nested
// epoch mutex.
func (c *fullRebuildLifecycleCache) totalSetAttempts() int {
	c.retirementRaceCache.mu.Lock()
	defer c.retirementRaceCache.mu.Unlock()
	var total int
	for _, attempts := range c.setAttempts {
		total += attempts
	}
	return total
}

// ListActiveIDs increments lightweight discovery count and copies the
// configured ID slice under mu. The configured error accompanies that copy,
// making reader selection and global discovery failure observable separately.
func (r *fullRebuildLifecycleGroupRepo) ListActiveIDs(context.Context) ([]int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activeIDCalls++
	return append([]int64(nil), r.activeIDs...), r.activeIDsErr
}

// ListActive records fallback discovery and returns no groups with its
// configured error. This lightweight-capable fixture uses active IDs instead,
// so tests can detect an incorrectly attempted fallback call.
func (r *fullRebuildLifecycleGroupRepo) ListActive(context.Context) ([]SnapshotGroup, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listActiveCalls++
	return nil, r.listActiveErr
}

// GetByIDLite records the requested group ID and returns its configured fresh
// error or a copied group value. Absent mapped groups return the scheduler
// missing-group sentinel, allowing authoritative retirement tests.
func (r *fullRebuildLifecycleGroupRepo) GetByIDLite(_ context.Context, id int64) (*SnapshotGroup, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.freshCalls = append(r.freshCalls, id)
	if err := r.freshErr[id]; err != nil {
		return nil, err
	}
	group := r.fresh[id]
	if group == nil {
		return nil, ErrSnapshotGroupNotFound
	}
	copyGroup := *group
	return &copyGroup, nil
}

// stats returns discovery counts and a copied fresh-query ID list under mu.
// The caller receives independent slice storage for the recorded calls while
// configured group records remain fixture-owned.
func (r *fullRebuildLifecycleGroupRepo) stats() (activeIDs, listActive int, fresh []int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activeIDCalls, r.listActiveCalls, append([]int64(nil), r.freshCalls...)
}

// ListActive increments fallback discovery count and copies configured group
// values under mu. Its configured error is returned unchanged; the type
// intentionally omits the lightweight active-ID interface.
func (r *fullRebuildFallbackGroupRepo) ListActive(context.Context) ([]SnapshotGroup, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listCalls++
	return append([]SnapshotGroup(nil), r.groups...), r.err
}

// record appends a group/platform source observation and captures beforeFirst
// under mu. It then invokes that hook at most once through once outside the
// mutex, so a hook can inspect prepared cache tokens.
//
// Every read returns one synthetic active provider with the representative
// platform and fixed ID. The hook precedes that return and can expose the
// first query to ordering assertions; later concurrent reads still record
// calls even when the first hook is blocked. Configure the hook before reads
// because callers supply its lifetime.
func (r *fullRebuildProviderRepo) record(groupID int64, platform string) ([]SnapshotProvider, error) {
	r.mu.Lock()
	r.calls = append(r.calls, fullRebuildProviderCall{groupID: groupID, platform: platform})
	beforeFirst := r.beforeFirst
	r.mu.Unlock()
	// The first-read hook runs after call recording and outside mu. Its once
	// guard exposes the preparation boundary while other calls retain their own
	// query observations.
	if beforeFirst != nil {
		r.once.Do(beforeFirst)
	}
	return []SnapshotProvider{snapshotTestProvider{ID: 1, Platform: platform, Status: StatusActive, Schedulable: true}}, nil
}

// ListSchedulableByGroupIDAndPlatform delegates grouped single-platform query
// recording and synthetic data generation.
func (r *fullRebuildProviderRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]SnapshotProvider, error) {
	return r.record(groupID, platform)
}

// ListSchedulableByGroupIDAndPlatforms records a grouped query using
// firstPlatform's representative input. An empty platform list maps to the
// empty platform, and all other list members are ignored by this fixture.
func (r *fullRebuildProviderRepo) ListSchedulableByGroupIDAndPlatforms(_ context.Context, groupID int64, platforms []string) ([]SnapshotProvider, error) {
	return r.record(groupID, firstPlatform(platforms))
}

// ListSchedulableUngroupedByPlatform delegates query recording using group
// zero and the platform.
func (r *fullRebuildProviderRepo) ListSchedulableUngroupedByPlatform(_ context.Context, platform string) ([]SnapshotProvider, error) {
	return r.record(0, platform)
}

// ListSchedulableUngroupedByPlatforms records group zero and a representative
// first platform. Empty input uses the shared empty platform, while record
// owns the first-read hook and synthetic provider result.
func (r *fullRebuildProviderRepo) ListSchedulableUngroupedByPlatforms(_ context.Context, platforms []string) ([]SnapshotProvider, error) {
	return r.record(0, firstPlatform(platforms))
}

// ListModelAvailabilityCandidates panics if full-rebuild tests reach
// model-availability lookup. These fixtures expose schedulable readers for
// snapshot reconciliation, so this unsupported call identifies an unexpected
// path instead of returning plausible provider data.
func (r *fullRebuildProviderRepo) ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]SnapshotProvider, error) {
	panic("unexpected ListModelAvailabilityCandidates call")
}

// ListSchedulableByPlatform delegates representative platform recording with a
// group-zero identity.
func (r *fullRebuildProviderRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]SnapshotProvider, error) {
	return r.record(0, platform)
}

// ListSchedulableByPlatforms records group-zero source work using
// firstPlatform's representative value. Empty input maps to the shared
// platform, and record supplies the synthetic provider and optional first-read
// observation.
func (r *fullRebuildProviderRepo) ListSchedulableByPlatforms(_ context.Context, platforms []string) ([]SnapshotProvider, error) {
	return r.record(0, firstPlatform(platforms))
}

// callCount returns the fixture's total recorded provider query count.
func (r *fullRebuildProviderRepo) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// groupCallCount returns recorded provider queries matching the supplied group
// ID.
func (r *fullRebuildProviderRepo) groupCallCount(groupID int64) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	var count int
	for _, call := range r.calls {
		if call.groupID == groupID {
			count++
		}
	}
	return count
}

// firstPlatform returns the first supplied platform or empty when no input
// exists. The fixtures use this representative value for plural-query
// observations instead of preserving every queried platform.
func firstPlatform(platforms []string) string {
	if len(platforms) == 0 {
		return ""
	}
	return platforms[0]
}

// newFullRebuildLifecycleService binds supplied cache, outbox, provider, and
// group fixtures with empty options. Full reconciliation requires the
// configured group reader, while optional outbox input allows direct or
// poll-driven tests.
func newFullRebuildLifecycleService(
	cache SnapshotCache,
	outbox SchedulerOutboxRepository,
	providers SnapshotProviderSource, groups SnapshotGroupSource,
) *SnapshotService {
	return NewSnapshotService(cache, outbox, providers, groups, &SnapshotOptions{})
}

// requireFullRebuildNoMutationOrDB checks zero capture, retirement, reopen,
// publication, and provider-read observations after discovery failure. It
// marks itself as a test helper so failed expectations point to the calling
// case.
func requireFullRebuildNoMutationOrDB(t *testing.T, cache *fullRebuildLifecycleCache, providers *fullRebuildProviderRepo) {
	t.Helper()
	require.Zero(t, cache.captureAttemptCount())
	require.Empty(t, cache.retiredBuckets())
	require.Empty(t, cache.tokens())
	require.Zero(t, providers.callCount())
	require.Zero(t, cache.totalSetAttempts())
}

// AcquireBucketLease delegates outer one-shot contention and base acquisition
// recording, then wraps accepted ownership in BucketLease. Unaccepted results
// carry no lease, and successful cleanup invokes the inherited UnlockBucket
// callback.
func (c *fullRebuildLifecycleCache) AcquireBucketLease(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (*BucketLease, bool, error) {
	ok, err := c.TryLockBucket(ctx, bucket, ttl)
	if err != nil || !ok {
		return nil, ok, err
	}
	return NewBucketLease(func(cleanup context.Context) error { return c.UnlockBucket(cleanup, bucket) }), true, nil
}

// ListBuckets records registry discovery under mu and returns a nil bucket
// list with the configured error. A nil configured error represents successful
// empty discovery; the fixture ignores caller context cancellation.
func (c *schedulerFullRebuildTestCache) ListBuckets(context.Context) ([]SchedulerBucket, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listCalls++
	return nil, c.listErr
}

// TryLockBucket records every acquisition attempt under mu and returns false
// with no error. This permanent contention makes ordinary rebuilding skip
// publication; group, lifetime, and caller cancellation inputs do not affect
// the fixture result.
func (c *schedulerFullRebuildTestCache) TryLockBucket(context.Context, SchedulerBucket, time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lockCalls++
	return false, nil
}

// CaptureBucketWriteToken counts a capture and returns constant epoch one for
// the requested bucket. Context is ignored; these observations isolate
// discovery/coalescing work from real epoch storage behavior.
func (c *schedulerFullRebuildTestCache) CaptureBucketWriteToken(_ context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	c.mu.Lock()
	c.captures++
	c.mu.Unlock()
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}

// ReopenBucket returns the supplied bucket with constant fixture epoch one and
// no error. It records no lifecycle mutation and ignores context, so its token
// cannot establish real retirement/reopen generation behavior.
func (c *schedulerFullRebuildTestCache) ReopenBucket(_ context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}

// schedulerFullRebuildState samples requested and completed generations under
// the service's state mutex.
func schedulerFullRebuildState(svc *SnapshotService) (requested uint64, completed uint64) {
	svc.fullRebuildStateMu.Lock()
	defer svc.fullRebuildStateMu.Unlock()
	return svc.fullRebuildRequested, svc.fullRebuildCompleted
}

// AcquireBucketLease returns this fixture's denied bucket acquisition with no
// lease. Its TryLockBucket always reports false, so direct full-rebuild tests
// observe skip policy while no release callback can be required.
func (c *schedulerFullRebuildTestCache) AcquireBucketLease(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (*BucketLease, bool, error) {
	ok, err := c.TryLockBucket(ctx, bucket, ttl)
	return nil, ok, err
}

// newGroupLifecycleTestCache constructs an epoch-fencing fixture with
// initially unheld global lifecycle ownership. It retains supplied registry
// buckets in the nested cache, and tests configure authority, mutation, or
// acquisition faults before use.
func newGroupLifecycleTestCache(buckets ...SchedulerBucket) *groupLifecycleTestCache {
	return &groupLifecycleTestCache{retirementRaceCache: newRetirementRaceCache(buckets...)}
}

// TryAcquireGroupLifecycleLease records acquisition duration and deadline
// presence under stateMu before applying injected errors or contention. It
// models one global fixture owner, so an already held lease rejects another
// acquisition regardless of group. Accepted calls create a sequential owner
// token.
//
// The method records the requested group in that owner value, but does not
// simulate TTL expiration or reject an already canceled context. Tests use the
// observations to verify caller-supplied budgets and release order. Successful
// fixture ownership stays held until a matching successful
// ReleaseGroupLifecycleLease clears it.
func (c *groupLifecycleTestCache) TryAcquireGroupLifecycleLease(ctx context.Context, groupID int64, ttl time.Duration) (SchedulerGroupLifecycleLease, bool, error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.acquireCalls++
	c.acquireTTL = ttl
	_, c.acquireDeadline = ctx.Deadline()
	if c.leaseAcquireErr != nil {
		return SchedulerGroupLifecycleLease{}, false, c.leaseAcquireErr
	}
	if c.leaseBusy || c.leaseHeld {
		return SchedulerGroupLifecycleLease{}, false, nil
	}
	// Accepted ownership receives a fresh synthetic owner token before held state
	// is exposed. Matching release can then distinguish this owner from stale or
	// missing fixture ownership.
	c.leaseSequence++
	c.lease = SchedulerGroupLifecycleLease{GroupID: groupID, OwnerToken: fmt.Sprintf("owner-%d", c.leaseSequence)}
	c.leaseHeld = true
	return c.lease, true, nil
}

// ReleaseGroupLifecycleLease records release count, deadline presence, and
// current context error before validating ownership. An injected error returns
// with the owner still held, and a missing/mismatched owner returns
// lease-lost. Tests therefore inspect cleanup attempts separately from
// successful release.
//
// Matching release clears the global held flag under stateMu. The caller can
// pass an independent live cleanup context after authority cancellation, and
// the captured releaseCtxErr makes that distinction observable. This fixture
// performs no retry or idempotence check of its own beyond validating the
// current owner value.
func (c *groupLifecycleTestCache) ReleaseGroupLifecycleLease(ctx context.Context, lease SchedulerGroupLifecycleLease) error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	// Record the cleanup context before failure or owner validation. A canceled
	// authority operation can still prove that its independent release attempt
	// had a live deadline.
	c.releaseCalls++
	_, c.releaseDeadline = ctx.Deadline()
	c.releaseCtxErr = ctx.Err()
	if c.leaseReleaseErr != nil {
		return c.leaseReleaseErr
	}
	if !c.leaseHeld || lease != c.lease {
		return ErrSchedulerGroupLifecycleLeaseLost
	}
	c.leaseHeld = false
	return nil
}

// RetireBucket records its bucket and current owner-held state under stateMu,
// then delegates epoch retirement outside that lock. It rejects calls observed
// without lifecycle ownership. Injected retireErr fails every call for a
// nonpositive ordinal or only the configured absolute call when positive.
//
// Failed attempts remain in retireCalls and retireHeld, allowing
// partial-mutation ordering to be checked. Delegated retirement uses the
// nested cache's epoch mutex and retains prior publication counters. The two
// fixture locks do not make observed owner state and the subsequent epoch
// mutation one atomic operation; service tests arrange their lease-controlled
// sequence.
func (c *groupLifecycleTestCache) RetireBucket(ctx context.Context, bucket SchedulerBucket) error {
	c.stateMu.Lock()
	// Capture the owner-held state with this mutation ordinal before delegating
	// epoch work. Failed attempts remain observable, including failures after
	// earlier buckets were already retired.
	c.retireCalls = append(c.retireCalls, bucket)
	c.retireHeld = append(c.retireHeld, c.leaseHeld)
	held := c.leaseHeld
	call := len(c.retireCalls)
	err := c.retireErr
	errAt := c.retireErrAt
	c.stateMu.Unlock()
	if !held {
		return errors.New("retire called outside group lifecycle lease")
	}
	if err != nil && (errAt <= 0 || call == errAt) {
		return err
	}
	return c.retirementRaceCache.RetireBucket(ctx, bucket)
}

// ReopenBucket rejects a completed caller context before recording owner
// state, then requires held global lifecycle ownership. Injected reopenErr
// applies to every attempt for a nonpositive ordinal or the configured
// absolute attempt when positive. Accepted calls delegate epoch reopening
// outside stateMu.
//
// Only successful nested results enter reopenTokens after reacquiring the
// observation mutex. reopenHeld still records failed configured attempts,
// distinguishing observed owner state from tokens ready for queries. The epoch
// and owner-state locks remain separate, and tests arrange the service's
// lease-controlled ordering between them.
func (c *groupLifecycleTestCache) ReopenBucket(ctx context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	if err := ctx.Err(); err != nil {
		return SchedulerBucketWriteToken{}, err
	}
	c.stateMu.Lock()
	// Record ownership before a configured mutation failure, then retain tokens
	// only after nested reopen success. Attempted owner states and usable query
	// tokens consequently have separate logs.
	c.reopenHeld = append(c.reopenHeld, c.leaseHeld)
	held := c.leaseHeld
	call := len(c.reopenHeld)
	reopenErr := c.reopenErr
	reopenErrAt := c.reopenErrAt
	c.stateMu.Unlock()
	if !held {
		return SchedulerBucketWriteToken{}, errors.New("reopen called outside group lifecycle lease")
	}
	if reopenErr != nil && (reopenErrAt <= 0 || call == reopenErrAt) {
		return SchedulerBucketWriteToken{}, reopenErr
	}
	token, err := c.retirementRaceCache.ReopenBucket(ctx, bucket)
	if err != nil {
		return SchedulerBucketWriteToken{}, err
	}
	c.stateMu.Lock()
	c.reopenTokens = append(c.reopenTokens, token)
	c.stateMu.Unlock()
	return token, nil
}

// ListBuckets records registry discovery and snapshots its configured error
// under stateMu. Failure returns before the nested registry reader; success
// delegates a copied slice of the race cache's configured identities.
func (c *groupLifecycleTestCache) ListBuckets(ctx context.Context) ([]SchedulerBucket, error) {
	c.stateMu.Lock()
	c.listCalls++
	err := c.listErr
	c.stateMu.Unlock()
	if err != nil {
		return nil, err
	}
	return c.retirementRaceCache.ListBuckets(ctx)
}

// TryLockBucket records the requested lease duration under stateMu, returning
// its configured error or busy status. It does not retain per-bucket held
// ownership, so these observations protect caller ordering rather than
// distributed lock exclusion.
func (c *groupLifecycleTestCache) TryLockBucket(_ context.Context, _ SchedulerBucket, ttl time.Duration) (bool, error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.bucketLockTTLs = append(c.bucketLockTTLs, ttl)
	if c.bucketLockErr != nil {
		return false, c.bucketLockErr
	}
	return !c.bucketLockBusy, nil
}

// UnlockBucket records one successful bucket cleanup call under the
// observation mutex.
func (c *groupLifecycleTestCache) UnlockBucket(context.Context, SchedulerBucket) error {
	c.stateMu.Lock()
	// Count release callback execution so failed provider or publication paths
	// can be matched to the bucket lease they acquired earlier.
	c.unlockCalls++
	c.stateMu.Unlock()
	return nil
}

// SetSnapshot applies a configured pre-publication error sampled under
// stateMu. With no injection it delegates token/retirement checks and
// publication counts to retirementRaceCache, whose mutex owns those
// observations separately.
func (c *groupLifecycleTestCache) SetSnapshot(ctx context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, providers []SnapshotProvider) error {
	c.stateMu.Lock()
	err := c.setErr
	c.stateMu.Unlock()
	if err != nil {
		return err
	}
	return c.retirementRaceCache.SetSnapshot(ctx, bucket, token, providers)
}

// lifecycleCounts samples owner acquisition, release, and registry counts
// under stateMu.
func (c *groupLifecycleTestCache) lifecycleCounts() (acquires, releases, listCalls int) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.acquireCalls, c.releaseCalls, c.listCalls
}

// retiredBuckets copies recorded retirement attempt identities under stateMu,
// including failed attempts. An empty log returns nil; the returned bucket
// values can be modified without changing the fixture's mutation records.
func (c *groupLifecycleTestCache) retiredBuckets() []SchedulerBucket {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return append([]SchedulerBucket(nil), c.retireCalls...)
}

// tokens copies successfully reopened token values under stateMu and returns
// nil for an empty log. These bucket/epoch values can be inspected or modified
// independently of the fixture's retained token observations.
func (c *groupLifecycleTestCache) tokens() []SchedulerBucketWriteToken {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return append([]SchedulerBucketWriteToken(nil), c.reopenTokens...)
}

// leaseHeldAndTokenCount samples fixture owner state and successful reopen
// count together.
func (c *groupLifecycleTestCache) leaseHeldAndTokenCount() (bool, int) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.leaseHeld, len(c.reopenTokens)
}

// lockStats samples unlock count and copies observed bucket lease durations
// under stateMu. Returned duration storage is independent of the fixture, so
// cleanup assertions can inspect it after service work completes.
func (c *groupLifecycleTestCache) lockStats() ([]time.Duration, int) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return append([]time.Duration(nil), c.bucketLockTTLs...), c.unlockCalls
}

// lifecycleMutationLeaseStates copies held-state observations for retirement
// and reopen attempts under stateMu. Both returned slices can be modified
// independently, and their ordinals correspond to each mutation log's recorded
// attempt sequence.
func (c *groupLifecycleTestCache) lifecycleMutationLeaseStates() (retire, reopen []bool) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return append([]bool(nil), c.retireHeld...), append([]bool(nil), c.reopenHeld...)
}

// GetByIDLite counts reads and copies one configured authority value under mu,
// ignoring the requested ID so mismatch fixtures are possible. Configured
// errors and absent groups return before afterGet; the absent case uses the
// scheduler missing-group sentinel.
//
// For a successful value, the captured hook runs after unlocking and before
// the copied result returns. A test can therefore cancel the operation after
// its authority read but before the service mutates buckets. The original
// group pointer stays borrowed while configured; set replaces pointer/error
// together for subsequent reads.
func (r *groupLifecycleTestGroupRepo) GetByIDLite(context.Context, int64) (*SnapshotGroup, error) {
	r.mu.Lock()
	r.calls++
	if r.err != nil {
		err := r.err
		r.mu.Unlock()
		return nil, err
	}
	if r.group == nil {
		r.mu.Unlock()
		return nil, ErrSnapshotGroupNotFound
	}
	copyGroup := *r.group
	afterGet := r.afterGet
	r.mu.Unlock()
	// The authority copy is already fixed and mu released before the hook.
	// Cancellation here tests the service's next mutation and independent cleanup
	// phases.
	if afterGet != nil {
		afterGet()
	}
	return &copyGroup, nil
}

// set replaces configured group/error together under the source mutex for
// later authority reads. The group pointer is retained, so callers keep its
// fields stable while concurrent GetByIDLite copies them.
func (r *groupLifecycleTestGroupRepo) set(group *SnapshotGroup, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Replace the borrowed authority pointer and read error together under mu. A
	// subsequent lifecycle event then sees the intended active/inactive state
	// after the prior operation.
	r.group = group
	r.err = err
}

// callCount returns recorded Lite authority reads under the group-source
// mutex.
func (r *groupLifecycleTestGroupRepo) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// load records per-platform reads and captures error/start/release inputs
// under mu, then signals the first query through once. beforeLoadOnce executes
// the configured observation after that signal and before any release wait.
// Tests configure the hook before asynchronous calls.
//
// A nonnil release blocks until its channel or ctx.Done is selected;
// cancellation on that branch returns the context cause before the captured
// source error. Without a barrier, context is not checked here. A successful
// read returns one synthetic active provider, allowing tests to interleave
// retirement after source entry while retaining observed call counts.
func (r *groupLifecycleTestProviderRepo) load(ctx context.Context, platform string) ([]SnapshotProvider, error) {
	r.mu.Lock()
	r.calls++
	if r.callsByPlatform == nil {
		r.callsByPlatform = make(map[string]int)
	}
	r.callsByPlatform[platform]++
	err := r.err
	started := r.started
	release := r.release
	r.mu.Unlock()
	// Close the first-read signal at most once outside mu. Tests use that signal
	// to begin retirement after provider work starts without holding the source
	// observation lock.
	if started != nil {
		r.once.Do(func() { close(started) })
	}
	// Execute the configured preparation observation once after signaling query
	// entry. Its assertions can inspect lifecycle owner/token state before this
	// read waits or returns.
	if r.beforeLoad != nil {
		r.beforeLoadOnce.Do(r.beforeLoad)
	}
	// A configured barrier keeps provider work in flight while another event
	// changes bucket state. Selecting caller cancellation returns its cause
	// before any configured post-barrier source error.
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return []SnapshotProvider{snapshotTestProvider{ID: 9001, Platform: platform, Status: StatusActive, Schedulable: true}}, nil
}

// ListSchedulableByGroupIDAndPlatform delegates lifecycle query counting and
// barrier work for this platform.
func (r *groupLifecycleTestProviderRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, _ int64, platform string) ([]SnapshotProvider, error) {
	return r.load(ctx, platform)
}

// ListSchedulableByGroupIDAndPlatforms delegates query/barrier work using the
// first platform when supplied. Empty input uses the literal mixed fixture
// platform, and the group ID is ignored by this observation-only source.
func (r *groupLifecycleTestProviderRepo) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, _ int64, platforms []string) ([]SnapshotProvider, error) {
	platform := "mixed"
	if len(platforms) > 0 {
		platform = platforms[0]
	}
	return r.load(ctx, platform)
}

// callCount returns all lifecycle provider source reads under its mutex.
func (r *groupLifecycleTestProviderRepo) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// platformCallCount returns recorded source reads for the representative
// fixture platform.
func (r *groupLifecycleTestProviderRepo) platformCallCount(platform string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.callsByPlatform[platform]
}

// newGroupLifecycleTestService binds cache, provider, and authoritative group
// fixtures with empty options. Tests invoke lifecycle work directly without
// outbox polling, and the supplied reader determines active, inactive,
// missing, or failed authority.
func newGroupLifecycleTestService(cache SnapshotCache, providers SnapshotProviderSource, groups SnapshotGroupSource) *SnapshotService {
	return NewSnapshotService(cache, nil, providers, groups, &SnapshotOptions{})
}

// expectedGroupLifecycleBuckets enumerates expected single/forced identities
// for the supplied group across canonical/shared platforms. It keeps the ID as
// provided and serves as an assertion oracle for the current lifecycle bucket
// set.
func expectedGroupLifecycleBuckets(groupID int64) []SchedulerBucket {
	// The expected set has two modes; reserved capacity includes extra room.
	buckets := make([]SchedulerBucket, 0, len(schedulerSnapshotPlatforms())*3)
	for _, platform := range schedulerSnapshotPlatforms() {
		buckets = append(buckets,
			SchedulerBucket{GroupID: groupID, Platform: platform, Mode: SchedulerModeSingle},
			SchedulerBucket{GroupID: groupID, Platform: platform, Mode: SchedulerModeForced},
		)
	}
	return buckets
}

// expectedCanonicalProviderQueryCount returns one reusable provider query for
// each canonical/shared platform.
func expectedCanonicalProviderQueryCount() int {
	return len(schedulerSnapshotPlatforms())
}

// bucketStrings returns a set of serialized bucket identities for unordered
// assertions. Duplicate String values collapse to one entry, and callers use
// canonical or deliberate historical values with their intended encoding.
func bucketStrings(buckets []SchedulerBucket) map[string]struct{} {
	out := make(map[string]struct{}, len(buckets))
	for _, bucket := range buckets {
		out[bucket.String()] = struct{}{}
	}
	return out
}

// requireLifecycleSeen checks a completed lifecycle marker and every
// canonical/shared platform marker for the supplied group. It marks helper
// failures at the calling test and does not require the marker map to contain
// only that group.
func requireLifecycleSeen(t *testing.T, seen map[batchSeenKey]struct{}, groupID int64) {
	t.Helper()
	_, ok := seen[batchSeenKey{groupID: groupID, lifecycle: true}]
	require.True(t, ok)
	for _, platform := range schedulerSnapshotPlatforms() {
		_, ok = seen[batchSeenKey{groupID: groupID, platform: platform}]
		require.True(t, ok)
	}
}

// requireLifecycleNotSeen checks absence of lifecycle and all canonical/shared
// platform markers for one group. Other groups may remain in the map, and
// helper failures are attributed to the calling test.
func requireLifecycleNotSeen(t *testing.T, seen map[batchSeenKey]struct{}, groupID int64) {
	t.Helper()
	_, ok := seen[batchSeenKey{groupID: groupID, lifecycle: true}]
	require.False(t, ok)
	for _, platform := range schedulerSnapshotPlatforms() {
		_, ok = seen[batchSeenKey{groupID: groupID, platform: platform}]
		require.False(t, ok)
	}
}

// AcquireBucketLease wraps an accepted fixture bucket result in BucketLease
// with observed UnlockBucket cleanup. Failed or busy acquisitions return no
// handle, while successful service exits can be matched to the recorded unlock
// count.
func (c *groupLifecycleTestCache) AcquireBucketLease(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (*BucketLease, bool, error) {
	ok, err := c.TryLockBucket(ctx, bucket, ttl)
	if err != nil || !ok {
		return nil, ok, err
	}
	return NewBucketLease(func(cleanup context.Context) error { return c.UnlockBucket(cleanup, bucket) }), true, nil
}

// GetSnapshot reports an unavailable snapshot with no error for cleanup
// fixture calls. It ignores context and bucket input, leaving the service to
// apply its ordinary fallback policy if that path is exercised.
func (c *outboxCleanupCache) GetSnapshot(ctx context.Context, bucket SchedulerBucket) ([]SnapshotProvider, bool, error) {
	return nil, false, nil
}

// CaptureBucketWriteToken returns a bucket-associated token with constant
// fixture epoch one. The cleanup cache models accepted empty rebuild work
// rather than lifecycle fencing, and it ignores the caller context.
func (c *outboxCleanupCache) CaptureBucketWriteToken(ctx context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}

// SetSnapshot accepts cleanup-fixture publication with no recorded payload or
// version state.
func (c *outboxCleanupCache) SetSnapshot(ctx context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, providers []SnapshotProvider) error {
	return nil
}

// RetireBucket accepts cleanup-fixture retirement with no lifecycle or epoch
// mutation.
func (c *outboxCleanupCache) RetireBucket(ctx context.Context, bucket SchedulerBucket) error {
	return nil
}

// ReopenBucket supplies constant epoch one for a cleanup-fixture bucket. It
// does not simulate retired state or require an owner lease, so lifecycle
// authority tests use the dedicated lifecycle cache instead.
func (c *outboxCleanupCache) ReopenBucket(ctx context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}

// TryAcquireGroupLifecycleLease denies lifecycle ownership with no error in
// cleanup fixtures. Tests needing full group mutation use a different cache;
// this result allows contention handling without a lease to release.
func (c *outboxCleanupCache) TryAcquireGroupLifecycleLease(context.Context, int64, time.Duration) (SchedulerGroupLifecycleLease, bool, error) {
	return SchedulerGroupLifecycleLease{}, false, nil
}

// ReleaseGroupLifecycleLease reports successful cleanup-fixture release
// without an owner-state check.
func (c *outboxCleanupCache) ReleaseGroupLifecycleLease(context.Context, SchedulerGroupLifecycleLease) error {
	return nil
}

// GetProvider reports no cached provider and no error for cleanup fixture
// callers. Identity and context inputs are ignored, allowing only the
// service's missing-record recovery path to supply a result.
func (c *outboxCleanupCache) GetProvider(ctx context.Context, providerID int64) (SnapshotProvider, error) {
	return nil, nil
}

// SetProvider accepts cleanup-fixture provider updates without retaining the
// supplied record.
func (c *outboxCleanupCache) SetProvider(ctx context.Context, provider SnapshotProvider) error {
	return nil
}

// DeleteProvider accepts cleanup-fixture deletions without maintaining
// provider record state.
func (c *outboxCleanupCache) DeleteProvider(ctx context.Context, providerID int64) error {
	return nil
}

// UpdateLastUsed returns the configured usage publication error without
// recording timestamps. Tests prepare updateErr before serial event handling
// to observe whether a failed handler prevents watermark publication and
// cleanup.
func (c *outboxCleanupCache) UpdateLastUsed(ctx context.Context, updates map[int64]time.Time) error {
	return c.updateErr
}

// TryLockBucket grants every cleanup-fixture acquisition with no error or
// retained owner state. It ignores context, bucket, and duration inputs,
// allowing empty publication paths while providing no distributed exclusion
// behavior.
func (c *outboxCleanupCache) TryLockBucket(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (bool, error) {
	return true, nil
}

// UnlockBucket accepts cleanup-fixture bucket release without maintaining an
// owner record.
func (c *outboxCleanupCache) UnlockBucket(ctx context.Context, bucket SchedulerBucket) error {
	return nil
}

// ListBuckets increments the serial registry count and returns borrowed
// configured bucket storage with its error. This base cache has no mutex;
// callers keep configuration stable or provide external test coordination.
func (c *outboxCleanupCache) ListBuckets(ctx context.Context) ([]SchedulerBucket, error) {
	c.listBucketCalls++
	return c.listBuckets, c.listBucketErr
}

// GetOutboxWatermark returns the cleanup fixture's current serial consumption
// ID.
func (c *outboxCleanupCache) GetOutboxWatermark(ctx context.Context) (int64, error) {
	return c.watermark, nil
}

// SetOutboxWatermark assigns the serial watermark and appends its write
// observation.
func (c *outboxCleanupCache) SetOutboxWatermark(ctx context.Context, id int64) error {
	// Keep the serial cursor assignment and its observation together. Cleanup
	// tests inspect both after polling to verify publication before consumed-row
	// deletion.
	c.watermark = id
	c.setWatermarks = append(c.setWatermarks, id)
	return nil
}

// ListSchedulableUngroupedByPlatform returns an empty successful
// cleanup-fixture provider result.
func (r *outboxCleanupProviderRepo) ListSchedulableUngroupedByPlatform(context.Context, string) ([]SnapshotProvider, error) {
	return nil, nil
}

// ListBuckets counts registry calls under mu and blocks the first call outside
// that mutex. It closes started once because only the first recorded ordinal
// takes that branch, then waits for release without observing context
// cancellation.
//
// Later calls can return the stable inherited registry slice and error without
// waiting for the first. The test must open release to finish that first
// operation. This limited barrier exposes an in-flight degradation
// reservation; it does not synchronize inherited watermark or source event
// state by itself.
func (c *blockingOutboxCleanupCache) ListBuckets(context.Context) ([]SchedulerBucket, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.mu.Unlock()
	// Only the first recorded call owns the start signal and blocking wait.
	// Waiting outside mu leaves call-count access available while the rebuild
	// remains reserved.
	if call == 1 {
		close(c.started)
		<-c.release
	}
	return c.listBuckets, c.listBucketErr
}

// listCalls returns the blocking registry fixture's call count under its
// mutex.
func (c *blockingOutboxCleanupCache) listCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// ListAfterAndReleaseDedup returns visible fixture events beyond the supplied
// ID in configured slice order. A positive limit caps the result; nonpositive
// limits include all remaining events, and no dedup mutation is modeled.
func (r *outboxCleanupRepo) ListAfterAndReleaseDedup(ctx context.Context, afterID int64, limit int) ([]SchedulerOutboxEvent, error) {
	events := make([]SchedulerOutboxEvent, 0, len(r.events))
	for _, event := range r.events {
		if event.ID <= afterID {
			continue
		}
		events = append(events, event)
		if limit > 0 && len(events) >= limit {
			break
		}
	}
	return events, nil
}

// FirstCreatedAtAfter records the requested pending watermark and returns the
// first later event's creation time. Configured event order is retained, so
// tests supply the order rather than relying on timestamp sorting.
func (r *outboxCleanupRepo) FirstCreatedAtAfter(ctx context.Context, afterID int64) (time.Time, bool, error) {
	r.firstCreatedAfterID = append(r.firstCreatedAfterID, afterID)
	for _, event := range r.events {
		if event.ID > afterID {
			return event.CreatedAt, true, nil
		}
	}
	return time.Time{}, false, nil
}

// MaxID counts reads and returns the configured error or maximum retained row
// ID. The fixture rows are independent of its visible event list, allowing
// ID-distance and observation-failure cases to be modeled separately.
func (r *outboxCleanupRepo) MaxID(ctx context.Context) (int64, error) {
	r.maxIDCalls++
	if r.maxIDErr != nil {
		return 0, r.maxIDErr
	}
	var maxID int64
	for _, id := range r.rows {
		if id > maxID {
			maxID = id
		}
	}
	return maxID, nil
}

// DeleteConsumedUpTo records a deletion request and removes at most limit
// retained IDs at or below watermark in fixture order. Nonpositive watermark
// or limit returns zero without removal. Tests serialize access to this
// unsynchronized row slice.
//
// A successful call replaces rows with new storage retaining every unconsumed
// ID, including above-watermark entries. It does not simulate transaction
// commit grace or database locking; chunk count and remaining rows expose how
// poll cleanup repeats after watermark publication. Each call's recorded
// watermark and limit stay available for ordered assertions.
func (r *outboxCleanupRepo) DeleteConsumedUpTo(ctx context.Context, watermark int64, limit int) (int64, error) {
	r.deleteCalls = append(r.deleteCalls, outboxCleanupDeleteCall{
		watermark: watermark,
		limit:     limit,
	})
	if watermark <= 0 || limit <= 0 {
		return 0, nil
	}

	deleted := int64(0)
	kept := make([]int64, 0, len(r.rows))
	for _, id := range r.rows {
		if id <= watermark && deleted < int64(limit) {
			deleted++
			continue
		}
		kept = append(kept, id)
	}
	// Replace retained rows after counting this chunk's eligible deletions.
	// Above-watermark and excess same-watermark IDs survive for the next bounded
	// cleanup call.
	r.rows = kept
	return deleted, nil
}

// TryAcquireCleanupLock records one serial ownership attempt and uses
// lockAcquired to grant a callback lease. Denied calls return no lease;
// accepted release increments the fixture's release count without a stored
// owner token.
func (r *outboxCleanupRepo) TryAcquireCleanupLock(ctx context.Context) (SchedulerOutboxCleanupLease, bool, error) {
	r.lockAttempts++
	if !r.lockAcquired {
		return nil, false, nil
	}
	return outboxCleanupLease{release: func() {
		r.releaseCount++
	}}, true, nil
}

// Release invokes the optional cleanup observation callback, or does nothing
// when absent. Repeated calls invoke it repeatedly because this fixture
// wrapper has no once guard; service tests expect one release.
func (l outboxCleanupLease) Release() {
	if l.release != nil {
		l.release()
	}
}

// int64Range allocates an ascending inclusive ID range for deletion fixtures.
// Tests supply small ordered bounds whose difference and terminal increment
// fit int64; the helper performs no bound validation.
func int64Range(start, end int64) []int64 {
	values := make([]int64, 0, end-start+1)
	for id := start; id <= end; id++ {
		values = append(values, id)
	}
	return values
}

// AcquireBucketLease wraps this fixture's always-granted bucket acquisition in
// BucketLease cleanup. Its callback accepts UnlockBucket without ownership
// state, so cleanup-only tests can run prepared empty publications through the
// service.
func (c *outboxCleanupCache) AcquireBucketLease(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (*BucketLease, bool, error) {
	ok, err := c.TryLockBucket(ctx, bucket, ttl)
	if err != nil || !ok {
		return nil, ok, err
	}
	return NewBucketLease(func(cleanup context.Context) error { return c.UnlockBucket(cleanup, bucket) }), true, nil
}

// newRetirementRaceCache allocates epoch, retirement, attempt, publication,
// and version maps and retains supplied registry identities. Tests install the
// optional write hook before concurrency; unsupported embedded cache methods
// remain unavailable.
func newRetirementRaceCache(buckets ...SchedulerBucket) *retirementRaceCache {
	return &retirementRaceCache{
		epochs:      make(map[string]int64),
		retired:     make(map[string]bool),
		listBuckets: buckets,
		setAttempts: make(map[string]int),
		published:   make(map[string]int),
		versions:    make(map[string]int),
	}
}

// GetSnapshot reports an uncached retirement fixture bucket without an error.
// Context is ignored, allowing fallback tests to use source data while token
// capture and publication separately observe retirement.
func (c *retirementRaceCache) GetSnapshot(context.Context, SchedulerBucket) ([]SnapshotProvider, bool, error) {
	return nil, false, nil
}

// CaptureBucketWriteToken records the bucket before checking retirement under
// mu. Retired entries return a typed error; an absent epoch initializes to
// one, while an existing active epoch is retained.
//
// The returned token carries this bucket identity and its current epoch.
// Unlike batchSnapshotCache, repeated captures do not allocate a new epoch,
// allowing tests to attribute later fencing to retirement/reopen transitions.
// Context is ignored, and the observation list includes retired capture
// attempts even when no token is returned.
func (c *retirementRaceCache) CaptureBucketWriteToken(_ context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := bucket.String()
	c.captures = append(c.captures, bucket)
	if c.retired[key] {
		return SchedulerBucketWriteToken{}, ErrSchedulerBucketRetired
	}
	// Initialize only an absent fixture epoch; repeated active capture keeps it
	// stable. Retirement, rather than ordinary recapture, supplies the later
	// generation change tested by publication.
	if c.epochs[key] == 0 {
		c.epochs[key] = 1
	}
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: c.epochs[key]}, nil
}

// SetSnapshot invokes beforeSet outside mu, then records an attempt before
// validating identity, retirement, and epoch. Failed checks return typed
// causes before incrementing the fixture's version or accepted-publication
// count. Tests keep the hook configured before async use.
//
// Successful writes count membership publication without encoding or storing
// provider payloads. A barrier in beforeSet can consequently model retirement
// after database return but before the guarded version increment. The token
// and state are compared under the same mutex, making that specific fixture
// interleaving observable while actual Redis scripts remain outside this test
// double.
func (c *retirementRaceCache) SetSnapshot(_ context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, _ []SnapshotProvider) error {
	// Pause outside the epoch mutex before recording and validating this write.
	// Another test operation can retire the bucket before its guarded version
	// allocation.
	if c.beforeSet != nil {
		c.beforeSet()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := bucket.String()
	// Retain the attempted publication before identity/retirement/epoch
	// rejection. Advance version and accepted counts only after all checks,
	// exposing retirement-before-allocation behavior.
	c.setAttempts[key]++
	if !token.ValidFor(bucket) {
		return ErrSchedulerBucketWriteFenced
	}
	if c.retired[key] {
		return ErrSchedulerBucketRetired
	}
	if c.epochs[key] != token.Epoch {
		return ErrSchedulerBucketWriteFenced
	}
	c.versions[key]++
	c.published[key]++
	return nil
}

// RetireBucket changes an active fixture bucket to retired under mu, advancing
// its epoch once for that transition. A first uninitialized transition keeps
// the epoch at least one, and repeated retirement of an already retired bucket
// leaves it unchanged.
//
// Accepted-publication and version counters remain available as historical
// observations. Later reopening retains the advanced epoch, so an earlier
// active token can be rejected after an inactive/active cycle. This fixture
// mutation ignores context and does not remove stored payloads because payload
// storage is not represented here.
func (c *retirementRaceCache) RetireBucket(_ context.Context, bucket SchedulerBucket) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := bucket.String()
	// One active-to-retired transition advances the epoch under mu. Repeated
	// retirement leaves it stable, while later reopen cannot make the older
	// active token current again.
	if !c.retired[key] {
		c.epochs[key]++
		if c.epochs[key] < 1 {
			c.epochs[key] = 1
		}
		c.retired[key] = true
	}
	return nil
}

// ReopenBucket initializes an absent epoch to one, removes retirement state,
// and records a successful reopen under mu. It returns this bucket's existing
// epoch rather than allocating another epoch solely for activation. Context is
// ignored in this underlying fixture.
//
// A prior retirement supplies the generation change that fences tokens from an
// earlier active period. Reopening an already active fixture can therefore
// return the same epoch, while still adding another reopen observation.
// Lifecycle tests wrap this method with authority ownership and caller-context
// checks before accepting the returned token.
func (c *retirementRaceCache) ReopenBucket(_ context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := bucket.String()
	if c.epochs[key] == 0 {
		c.epochs[key] = 1
	}
	// Remove retirement while retaining the current epoch under mu. The prior
	// retirement's generation change remains available to reject tokens from an
	// earlier active period.
	delete(c.retired, key)
	c.reopens = append(c.reopens, bucket)
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: c.epochs[key]}, nil
}

// TryLockBucket grants every retirement-fixture acquisition with no error and
// ignores context and lifetime. Epoch publication safety comes from the
// fixture mutex; the returned success itself creates no exclusive bucket
// ownership.
func (c *retirementRaceCache) TryLockBucket(context.Context, SchedulerBucket, time.Duration) (bool, error) {
	return true, nil
}

// UnlockBucket reports successful retirement-fixture release without mutating
// lifecycle state.
func (c *retirementRaceCache) UnlockBucket(context.Context, SchedulerBucket) error {
	return nil
}

// ListBuckets returns a slice copy of the configured registry, or nil when
// it has no entries. Tests keep registry inputs stable; this base cache's
// retirement operations do not update that registry.
func (c *retirementRaceCache) ListBuckets(context.Context) ([]SchedulerBucket, error) {
	return append([]SchedulerBucket(nil), c.listBuckets...), nil
}

// counts samples publication attempts and accepted writes under the epoch
// mutex.
func (c *retirementRaceCache) counts(bucket SchedulerBucket) (setAttempts, published int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.setAttempts[bucket.String()], c.published[bucket.String()]
}

// captureAndReopenCounts samples token capture and reopen observations under
// the epoch mutex.
func (c *retirementRaceCache) captureAndReopenCounts() (captures, reopens int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.captures), len(c.reopens)
}

// version returns the accepted-publication version count for this serialized
// bucket identity.
func (c *retirementRaceCache) version(bucket SchedulerBucket) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.versions[bucket.String()]
}

// ListActive returns the borrowed configured group slice with its read error.
// The fixture has no mutex and no Lite authority override, so tests keep
// discovery inputs stable during its calls.
func (r *retirementGroupRepo) ListActive(context.Context) ([]SnapshotGroup, error) {
	return r.groups, r.err
}

// AcquireBucketLease wraps always-granted retirement-fixture acquisition in a
// BucketLease callback. Its cleanup delegates successful UnlockBucket; epoch
// safety in these tests comes from guarded publication state rather than
// exclusive bucket ownership.
func (c *retirementRaceCache) AcquireBucketLease(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (*BucketLease, bool, error) {
	ok, err := c.TryLockBucket(ctx, bucket, ttl)
	if err != nil || !ok {
		return nil, ok, err
	}
	return NewBucketLease(func(cleanup context.Context) error { return c.UnlockBucket(cleanup, bucket) }), true, nil
}

// GetSnapshot returns the caller's current context error and no snapshot hit.
// A live context therefore produces an ordinary miss; this stub does not
// itself initiate cancellation during cache access.
func (s schedulerSnapshotContextCacheStub) GetSnapshot(ctx context.Context, bucket SchedulerBucket) ([]SnapshotProvider, bool, error) {
	return nil, false, ctx.Err()
}

// ListSchedulableByPlatform increments the fixture's serial fallback count and
// returns no records.
func (r *schedulerSnapshotFallbackRepoStub) ListSchedulableByPlatform(ctx context.Context, platform string) ([]SnapshotProvider, error) {
	r.calls++
	return nil, nil
}

// ListSchedulableUngroupedByPlatform increments the fixture's serial ungrouped
// fallback count.
func (r *schedulerSnapshotFallbackRepoStub) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]SnapshotProvider, error) {
	r.calls++
	return nil, nil
}

// SnapshotMetadata copies fixture identity and clones GroupIDs with
// slices.Clone, preserving nil-versus-empty shape. Present membership entries
// have separate storage, so metadata consumers can change them independently
// while provider value copies may still share their original GroupIDs.
func (a snapshotTestProvider) SnapshotMetadata() SnapshotMetadata {
	// Clone membership slice storage when exposing metadata so consumer edits
	// cannot alter the fixture's borrowed GroupIDs through this returned value.
	return SnapshotMetadata{ID: a.ID, Name: a.Name, Platform: a.Platform, GroupIDs: slices.Clone(a.GroupIDs)}
}

// snapshotTestData returns supported fixture data as a pointer for assertions.
// A value input becomes a struct copy sharing GroupIDs, while pointer input is
// returned as supplied, including nil; callers provide a usable pointer before
// dereferencing. Other dynamic types panic.
func snapshotTestData(value SnapshotProvider) *snapshotTestProvider {
	switch v := value.(type) {
	case snapshotTestProvider:
		return &v
	case *snapshotTestProvider:
		return v
	default:
		panic("unexpected snapshot fixture")
	}
}

// ptrInt64 returns a fresh pointer containing this optional fixture group ID.
func ptrInt64(value int64) *int64 { return &value }

// ListSchedulableByPlatform returns a configured single-platform hook result
// verbatim when present. Otherwise it filters usable stable fixtures, creating
// slice storage when entries match and returning nil when none match. Default
// results retain provider objects and ignore context cancellation.
func (r *retirementProviderSource) ListSchedulableByPlatform(ctx context.Context, platform string) ([]SnapshotProvider, error) {
	if r.listPlatformFunc != nil {
		return r.listPlatformFunc(ctx, platform)
	}
	var out []SnapshotProvider
	for _, a := range r.providers {
		if a.SnapshotMetadata().Platform == platform {
			out = append(out, a)
		}
	}
	return out, nil
}

// ListSchedulableByGroupIDAndPlatform delegates single-platform filtering and
// its optional hook, ignoring group identity. Tests supply
// membership-appropriate objects because this fixture does not independently
// apply the requested group's membership filter.
func (r *retirementProviderSource) ListSchedulableByGroupIDAndPlatform(ctx context.Context, _ int64, platform string) ([]SnapshotProvider, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}

// ListSchedulableUngroupedByPlatform delegates the single-platform filter and
// optional context-aware hook. It does not test absence of memberships, so
// source objects are chosen by the test to represent the intended ungrouped
// result.
func (r *retirementProviderSource) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]SnapshotProvider, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}

// ListSchedulableByPlatforms filters configured input entries against any
// supplied platform and preserves their order. Each input entry is appended at
// most once, while repeated object references in the input remain repeated;
// the single-platform hook is bypassed.
func (r *retirementProviderSource) ListSchedulableByPlatforms(ctx context.Context, platforms []string) ([]SnapshotProvider, error) {
	var out []SnapshotProvider
	for _, a := range r.providers {
		for _, platform := range platforms {
			if a.SnapshotMetadata().Platform == platform {
				out = append(out, a)
				break
			}
		}
	}
	return out, nil
}

// ListSchedulableByGroupIDAndPlatforms delegates plural platform filtering
// while ignoring group identity. It uses configured objects and bypasses the
// single-platform hook, so tests supply membership-appropriate data before
// invoking this path.
func (r *retirementProviderSource) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, _ int64, platforms []string) ([]SnapshotProvider, error) {
	return r.ListSchedulableByPlatforms(ctx, platforms)
}

// ListSchedulableUngroupedByPlatforms delegates plural filtering over borrowed
// configured objects. Group membership is not filtered here; the caller's
// fixture setup supplies the intended ungrouped records and leaves them stable
// while reading.
func (r *retirementProviderSource) ListSchedulableUngroupedByPlatforms(ctx context.Context, platforms []string) ([]SnapshotProvider, error) {
	return r.ListSchedulableByPlatforms(ctx, platforms)
}

// ListBuckets sends a registry-read notification before optionally waiting on
// block or caller cancellation. Tests provide receiver/buffer space because
// the notification send is not cancellation-aware. With no block, it
// immediately returns the planned discovery error.
//
// When block is present, selecting ctx.Done returns the context cause instead;
// selecting the barrier reaches the planned error. This difference lets
// shutdown tests distinguish cancellation-aware work from ordinary startup
// failure. Tests keep channel configuration stable and release/join
// outstanding work after timing assertions so the fixture can finish.
func (s *planSnapshotCache) ListBuckets(ctx context.Context) ([]SchedulerBucket, error) {
	// Notify query entry before the optional wait, requiring buffered space or a
	// receiver. The following barrier is cancellation-aware; this initial
	// notification send is not.
	s.calls <- struct{}{}
	// A configured block models in-flight startup discovery. Context cancellation
	// can finish the worker before the test opens its barrier, making shutdown
	// responsiveness observable.
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, errors.New("snapshot probe failed")
}

// SnapshotMetadata returns this cache-update fixture's supplied provider
// identity value.
func (v updateSnapshotValue) SnapshotMetadata() SnapshotMetadata {
	return SnapshotMetadata{ID: v.id}
}

// SetProvider records the supplied provider interface before returning its
// configured error. Values stay borrowed and serially accessed, allowing
// forwarding assertions while a failed call remains visible in the fixture
// log.
func (c *updateSnapshotCache) SetProvider(_ context.Context, value SnapshotProvider) error {
	c.values = append(c.values, value)
	return c.err
}
