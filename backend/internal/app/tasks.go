package app

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/TokenFlux/TokenRouter/internal/apikey"
	keyhttp "github.com/TokenFlux/TokenRouter/internal/apikey/httpapi"
	keypostgres "github.com/TokenFlux/TokenRouter/internal/apikey/postgres"
	"github.com/TokenFlux/TokenRouter/internal/app/lifecycle"
	"github.com/TokenFlux/TokenRouter/internal/batchimage"
	batchhttp "github.com/TokenFlux/TokenRouter/internal/batchimage/httpapi"
	batchimageprovider "github.com/TokenFlux/TokenRouter/internal/batchimage/provider"
	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/creative"
	creativehttp "github.com/TokenFlux/TokenRouter/internal/creative/httpapi"
	gatewayhttp "github.com/TokenFlux/TokenRouter/internal/gateway/httpapi"
	providerpostgres "github.com/TokenFlux/TokenRouter/internal/provider/postgres"
)

// Creative managed keys receive generated identifiers and timestamps through
// the object passed to KeyStore.CreateManagedKey. The adapter copies that object
// before creation and copies it back afterward, including when the store returns
// an error. CopyAPIKey copies selected user and group views while leaving other
// reference fields shared.
//
// Batch image handlers use authenticated key views and the request's selected billing
// subscription to identify submissions and downloads. Their activity registration
// lets lifecycle shutdown reject new task calls and wait for existing responses
// before worker and cleanup hooks stop. Provider resolution for downloads and
// cleanup uses the supplied registry together with the persisted provider store.

// creativeManagedKeys adapts the application's KeyStore for creative managed keys.
type creativeManagedKeys struct {
	// store supplies managed-key lookup and creation; application lifecycle owns
	// its database connection and this adapter does not close it.
	store *keypostgres.KeyStore
}

// batchCleanupRuntime identifies the batch cleanup loop separately from worker loops.
type batchCleanupRuntime struct {
	// Runtime is the cleanup instance registered by provideJobsRuntime; its Start
	// and StopContext methods control the configured cleanup loop.
	*batchimage.Runtime
}

// taskRequestActivity tracks task HTTP calls until their handlers finish responding.
type taskRequestActivity struct {
	// Operations admits calls through bound handlers; the manager stops admission
	// and waits for their releases before later task and storage hooks.
	*lifecycle.Operations
}

// batchImageAccessPorts supplies request identity and billing selections to batch
// handlers. Key uses CopyAPIKey's selected-view copying rules; the preferred
// subscription is the borrowed context value and is available only in
// subscription billing mode with a nonnil subscription.
func batchImageAccessPorts() batchhttp.AccessPorts {
	return batchhttp.AccessPorts{Key: func(c *gin.Context) (*apikey.APIKey, bool) {
		k, ok := keyhttp.GetAPIKeyFromContext(c)
		return apikey.CopyAPIKey(k), ok
	}, PreferredSubscription: func(c *gin.Context) (*billing.UserSubscription, bool) {
		v, ok := gatewayhttp.GetAPIKeyBillingContext(c)
		if !ok || v == nil || v.Mode != apikey.APIKeyBillingModeSubscription || v.Subscription == nil {
			return nil, false
		}
		return v.Subscription, true
	}, SessionID: gatewayhttp.ExtractClientSessionID}
}

// GetManagedKeyByUserAndGroup looks up the user's key for groupID and managedBy.
// It returns a CopyAPIKey view and the store's error, including a nil view for
// missing keys; reference fields outside CopyAPIKey's selected copies remain shared.
func (keys creativeManagedKeys) GetManagedKeyByUserAndGroup(ctx context.Context, userID, groupID int64, managedBy string) (*apikey.APIKey, error) {
	key, err := keys.store.GetManagedKeyByUserAndGroup(ctx, userID, groupID, managedBy)
	return apikey.CopyAPIKey(key), err
}

// CreateManagedKey persists a copied key and returns the store's creation error.
// Callers supply ManagedBy and the other creation fields, and keep key stable
// for the duration of this call. After the store returns, the adapter replaces
// the caller's key with another CopyAPIKey view, carrying back generated IDs and
// timestamps on success. Copy-back also occurs on errors; copied user and group
// views can change identity while other reference fields remain shared. Nil key
// returns the store's required-key error.
func (keys creativeManagedKeys) CreateManagedKey(ctx context.Context, key *apikey.APIKey) error {
	view := apikey.CopyAPIKey(key)
	err := keys.store.CreateManagedKey(ctx, view)
	// Copy the creation view back even on errors, so the caller receives
	// the resulting object state together with the creation error.
	if key != nil && view != nil {
		*key = *apikey.CopyAPIKey(view)
	}
	return err
}

// provideBatchRegistry constructs Gemini API and Vertex batch provider adapters.
// Their constructors supply default HTTP and object-store clients for the nil
// arguments; cfg supplies Vertex options and nil cfg selects their zero values.
func provideBatchRegistry(cfg *config.Config) *batchimage.Registry[batchimageprovider.BatchImageProvider] {
	return batchimage.NewRegistry[batchimageprovider.BatchImageProvider](batchimageprovider.NewGeminiAPIBatchImageProvider(nil), batchimageprovider.NewVertexBatchImageProvider(batchVertexOptions(cfg), nil, nil, nil))
}

// provideCreativeHTTP constructs the creative handler and binds task admission.
// activity must be nonnil, and construction finishes before serving requests;
// the handler releases admitted activity after its response handling returns.
func provideCreativeHTTP(s *creative.Public, activity *taskRequestActivity) *creativehttp.CreativeHandler {
	h := creativehttp.NewCreativeHandler(s)
	h.BindActivity(activity.Enter)
	return h
}

// provideBatchHTTP constructs batch handlers with request identity and task admission.
// activity must be nonnil, and the binding precedes serving requests so each
// admitted submission, download, or management call remains tracked through its response.
func provideBatchHTTP(s *batchimage.Public, d *batchimage.Download, c *batchimage.Cleanup, activity *taskRequestActivity) *batchhttp.BatchImageHandler {
	h := batchhttp.NewBatchImageHandler(s, d, c, batchImageAccessPorts())
	h.BindActivity(activity.Enter)
	return h
}

// provideBatchDownload constructs provider-backed downloads with the supplied limiter.
// Non-nil cfg sets item, byte, and duration limits; nil cfg leaves zero options
// for Download's own defaults when a download runs.
func provideBatchDownload(repo batchimage.BatchImageRepository, providers *providerpostgres.ProviderStore, limiter batchimage.BatchImageDownloadLimiter, cfg *config.Config, registry *batchimage.Registry[batchimageprovider.BatchImageProvider]) *batchimage.Download {
	core := &batchimage.Download{Repo: repo, Limiter: limiter, ResolveProvider: (batchimageprovider.ResultAccess{Registry: registry, Providers: providers}).Download}
	if cfg != nil {
		// Convert the download budget from configuration seconds to a duration.
		core.Options = batchimage.DownloadOptions{MaxItems: cfg.BatchImage.MaxDownloadItemsZip, MaxBytes: cfg.BatchImage.MaxDownloadBytesPerRequest, Duration: time.Duration(cfg.BatchImage.MaxDownloadDurationSeconds) * time.Second}
	}
	return core
}

// provideBatchCleanup constructs cleanup with provider resolution, logging, and
// the system clock. Non-nil cfg supplies input retention, scan interval, and
// batch size; Cleanup applies its own defaults to nonpositive option values.
// Construction leaves loop startup to the registered cleanup runtime.
func provideBatchCleanup(repo batchimage.BatchImageRepository, providers *providerpostgres.ProviderStore, cfg *config.Config, registry *batchimage.Registry[batchimageprovider.BatchImageProvider]) *batchimage.Cleanup {
	core := &batchimage.Cleanup{Repo: repo, Now: time.Now, Observe: creativeObserve, ResolveProvider: (batchimageprovider.ResultAccess{Registry: registry, Providers: providers}).Cleanup}
	if cfg != nil {
		// Input retention uses configuration hours; scan intervals use configuration
		// minutes, matching Cleanup's duration-based comparisons and ticker.
		core.Options = batchimage.CleanupOptions{InputRetention: time.Duration(cfg.BatchImage.InputRetentionAfterTerminalHours) * time.Hour, Interval: time.Duration(cfg.BatchImage.CleanupIntervalMinutes) * time.Minute, BatchSize: cfg.BatchImage.CleanupBatchSize}
	}
	return core
}

// provideBatchCleanupRuntime prepares the cleanup loop for lifecycle startup.
// It enables execution when core has a repository, cfg enables batch images,
// and Cleanup's effective interval is positive; missing dependencies produce an
// inactive runtime whose Start does not invoke the bound loop.
func provideBatchCleanupRuntime(core *batchimage.Cleanup, cfg *config.Config) *batchCleanupRuntime {
	enabled := core != nil && core.Repo != nil && cfg != nil && cfg.BatchImage.Enabled && core.CleanupInterval() > 0
	return &batchCleanupRuntime{batchimage.NewRuntime("batch image cleanup", enabled, core.Run)}
}

// provideTaskActivity registers task HTTP admission and response draining with
// manager. It returns the activity shared by creative and batch handlers;
// StopOrder 16 places its bounded wait before task worker and cleanup hooks.
func provideTaskActivity(manager *lifecycle.Manager) *taskRequestActivity {
	activity := &taskRequestActivity{lifecycle.NewOperations("TaskRequestsAndDownloads")}
	manager.Register(lifecycle.Hook{Name: "TaskRequestsAndDownloads", StopOrder: 16, Stop: activity.StopContext})
	return activity
}
