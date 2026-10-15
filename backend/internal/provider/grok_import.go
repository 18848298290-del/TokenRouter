package provider

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// grok_import.go creates Grok providers from OAuth and SSO input and refreshes
// imported credentials.
// Grok SSO imports create providers independently and report partial success.
// Single and list inputs are split, normalized, and deduplicated before at most
// three workers receive indexed jobs. Each worker owns a distinct result slot,
// and joining all workers lets successful and failed items retain normalized
// input order despite different completion times.
//
// Temporary SSO values are exchanged before credential assembly. Request
// operational settings are filtered separately from exchanged token fields, and
// token-only authorization can impose provider expiry with automatic pause.
// Creation precedes probe scheduling, so a panic in that later callback can
// produce a failed item after a provider was saved. The task runner must start
// every worker asynchronously, and shared request maps remain stable through
// the batch.

// grokSSOImportConcurrency bounds simultaneous SSO exchange and provider
// creation tasks; indexed results retain input order after workers are joined.
const grokSSOImportConcurrency = 3

// ErrGrokSSOInput reports that normalization left no importable SSO values
// before any worker or provider creation is started.
var ErrGrokSSOInput = errors.New("sso_tokens is required")

// GrokImportInputError reports a public provider-import validation failure to
// administrators.
type GrokImportInputError struct {
	Message string /* public input failure text */
}

// GrokProviderImportOptions supplies provider persistence, token normalization,
// and batch task callbacks.
type GrokProviderImportOptions struct {
	// Get, Create, and Update are required when their import entry is reached.
	// Get and Create successes provide nonnil records before validation and
	// scheduling. Update passes its returned row directly to the caller.
	Get func(context.Context, int64) (*Record, error) // existing provider record reader
	// new provider persistence callback
	Create func(context.Context, *CreateProviderInput) (*Record, error)
	// existing provider credential updater
	Update func(context.Context, int64, *UpdateProviderInput) (*Record, error)
	// Schedule runs after Create succeeds. It needs to return normally for the
	// item to report creation; the panic wrapper can catch a later scheduling
	// panic.
	Schedule       func(*Record)       // post-create quota probe scheduler
	NormalizeToken func(string) string // temporary SSO token normalizer
	// RunTask starts every submitted worker asynchronously and runs its callback
	// through completion. Dispatch uses an unbuffered channel and a pre-added
	// completion barrier, so omitted or synchronous callbacks can prevent
	// progress.
	RunTask func(string, func()) // asynchronous batch worker runner
	// LogError handles recovered panics and needs to return normally because the
	// deferred recovery calls it while already handling the original worker
	// failure.
	LogError func(string, ...any) // panic recovery diagnostic callback
}

// GrokProviderImport exchanges authorization inputs and coordinates
// administrator provider creation or renewal.
type GrokProviderImport struct {
	Authorization *GrokAuthorization // retained OAuth exchange service
	// Options retains supplied callbacks without defaulting. Configure reached
	// entries before use and keep those callbacks stable while independent batch
	// workers share the import service.
	Options GrokProviderImportOptions // retained provider import callbacks
}

// GrokOAuthProviderCreateInput combines browser proof with provider creation
// settings.
type GrokOAuthProviderCreateInput struct {
	// SessionID, Code, State, and RedirectURI describe saved browser completion,
	// while Name configures its resulting provider. Exchange validates proof
	// before CreateFromOAuth chooses a display name and writes settings.
	// SessionID: saved browser flow identity; Code: returned authorization code;
	// State: saved session proof; RedirectURI: submitted callback URI;
	// Name: provider display name.
	SessionID, Code, State, RedirectURI, Name string
	ProxyID                                   *int64 // optional selected proxy identity
	// requested concurrency limit and scheduling priority value
	Concurrency, Priority int
	// GroupIDs is passed to the single Create callback as supplied. Callers
	// stabilize its slice during creation; SSO workers use a separately copied
	// group slice.
	GroupIDs []int64 // provider group assignment IDs
}

// GrokSSOToOAuthRequest carries temporary SSO inputs and shared provider
// creation settings.
type GrokSSOToOAuthRequest struct {
	// SSOTokens and SSOToken are normalized into one ordered list with the single
	// input first. Blank and duplicate normalized values are omitted before
	// indexed jobs are created.
	SSOTokens []string `json:"sso_tokens"` // batch temporary SSO values
	SSOToken  string   `json:"sso_token"`  // single temporary SSO value
	Name      string   `json:"name"`       // requested provider display base
	Notes     *string  `json:"notes"`      // optional provider operator notes
	ProxyID   *int64   `json:"proxy_id"`   // optional selected proxy identity
	// GroupIDs is copied separately for each Create input. Shared request slices
	// and maps remain stable until every batch worker finishes reading or cloning
	// them.
	GroupIDs []int64 `json:"group_ids"` // provider group assignment IDs
	// Credentials contributes allowlisted operational settings, with exchanged
	// token fields taking precedence except for a nonblank requested base URL. The
	// merge can borrow nested operational values.
	Credentials map[string]any `json:"credentials"` // requested operational credential settings
	// Extra is cloned by CloneGrokSSOMap for each Create input. String-key maps
	// and interface slices detach recursively, while other mutable values retain
	// that helper's sharing.
	Extra          map[string]any `json:"extra"`           // requested provider metadata fields
	Concurrency    int            `json:"concurrency"`     // requested provider concurrency limit
	LoadFactor     *int           `json:"load_factor"`     // optional provider load factor
	Priority       int            `json:"priority"`        // requested scheduling priority value
	RateMultiplier *float64       `json:"rate_multiplier"` // optional provider billing multiplier
	// ExpiresAt and AutoPauseOnExpired normally retain the supplied pointers. A
	// positive token expiry without a refresh token forces automatic pause and
	// uses the earlier positive requested expiry when present.
	ExpiresAt          *int64 `json:"expires_at"`            // Unix-seconds
	AutoPauseOnExpired *bool  `json:"auto_pause_on_expired"` // requested provider expiry pause
}

// GrokSSOToOAuthItemResult reports one normalized input's creation result or
// public failure.
type GrokSSOToOAuthItemResult struct {
	Index int    `json:"index"`           // one-based normalized input index
	Name  string `json:"name,omitempty"`  // selected provider display name
	Email string `json:"email,omitempty"` // exchanged account email metadata
	// Provider is present on a worker's successful creation result and is returned
	// as the Create callback supplied it. A recovered later panic can instead
	// report failure after storage succeeded.
	Provider *Record `json:"provider,omitempty"` // created provider record pointer
	Error    string  `json:"error,omitempty"`    // public item failure diagnostic
}

// GrokSSOToOAuthResponse separates ordered successful and failed normalized
// import items.
type GrokSSOToOAuthResponse struct {
	// Created and Failed partition the worker result slots after all workers
	// finish. Each list preserves normalized input order within its outcome, while
	// their indexes refer to the original combined list.
	Created []GrokSSOToOAuthItemResult `json:"created"` // successful items in input order
	Failed  []GrokSSOToOAuthItemResult `json:"failed"`  // failed items in input order
}

// grokSSOImportJob pairs temporary normalized authorization input with its
// result slot.
type grokSSOImportJob struct {
	index int    // zero-based worker result slot
	token string // normalized temporary SSO text
}

// grokSSOImportWorkerResult retains the worker outcome used for ordered
// response partitioning.
type grokSSOImportWorkerResult struct {
	// created selects the response partition for item. Panic recovery replaces the
	// result with a failed item, including when persistence succeeded before a
	// later callback panicked.
	created bool                     // successful worker creation outcome
	item    GrokSSOToOAuthItemResult // public normalized import outcome
}

// Error returns the public import validation message supplied by the caller.
func (e *GrokImportInputError) Error() string { return e.Message }

// NewGrokProviderImport retains authorization and provider callbacks for later
// administrator imports. It starts no worker and supplies no default callbacks;
// owners configure each reached dependency before invoking its entry.
func NewGrokProviderImport(auth *GrokAuthorization, options GrokProviderImportOptions) *GrokProviderImport {
	return &GrokProviderImport{Authorization: auth, Options: options}
}

// safeCreateProviderFromSSOToken returns one indexed creation result and
// converts a recovered worker panic into a public failed item. Recovery logs
// the panic type through a required nonpanicking callback; earlier accepted
// storage effects can remain.
func (h *GrokProviderImport) safeCreateProviderFromSSOToken(ctx context.Context, req GrokSSOToOAuthRequest, token string, index, total int) (result grokSSOImportWorkerResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			// Panic values may contain SSO tokens or request data; log the value's type to avoid disclosure.
			h.Options.LogError("grok_sso_import_worker_panic", "index", index, "panic_type", fmt.Sprintf("%T", recovered))
			result = grokSSOImportWorkerResult{
				item: GrokSSOToOAuthItemResult{
					Index: index,
					Error: "internal worker panic",
				},
			}
		}
	}()
	return h.createProviderFromSSOToken(ctx, req, token, index, total)
}

// createProviderFromSSOToken exchanges one normalized SSO value and creates its
// configured OAuth provider. It copies supported request metadata and group IDs
// before Create, then schedules quota probing only after successful creation;
// exchange or write errors become indexed item failures.
func (h *GrokProviderImport) createProviderFromSSOToken(ctx context.Context, req GrokSSOToOAuthRequest, token string, index, total int) grokSSOImportWorkerResult {
	tokenInfo, err := h.Authorization.ConvertFromSSO(ctx, token, req.ProxyID)
	if err != nil {
		return grokSSOImportWorkerResult{item: GrokSSOToOAuthItemResult{Index: index, Error: GrokSSOImportErrorMessage(err)}}
	}

	credentials := GrokSSOImportCredentials(h.Authorization.BuildProviderCredentials(tokenInfo), req.Credentials)
	name := GrokSSOImportProviderName(req.Name, tokenInfo, index, total)
	expiresAt, autoPauseOnExpired := GrokSSOImportExpiry(req.ExpiresAt, req.AutoPauseOnExpired, tokenInfo)
	provider, err := h.Options.Create(ctx, &CreateProviderInput{
		Name: name,

		Notes: req.Notes,

		Platform: PlatformGrok,

		Type: ProviderTypeOAuth,

		Credentials: credentials,

		Extra: CloneGrokSSOMap(req.Extra),

		ProxyID: req.ProxyID,

		Concurrency: req.Concurrency,

		LoadFactor: req.LoadFactor,

		Priority: req.Priority,

		RateMultiplier: req.RateMultiplier,

		GroupIDs: append([]int64(nil), req.GroupIDs...),

		ExpiresAt: expiresAt,

		AutoPauseOnExpired: autoPauseOnExpired,
	})
	if err != nil {
		return grokSSOImportWorkerResult{item: GrokSSOToOAuthItemResult{Index: index, Name: name, Email: tokenInfo.Email, Error: GrokSSOImportErrorMessage(err)}}
	}
	// Persisted creation precedes background probing. A later scheduling panic
	// can be reported by the worker wrapper while the saved provider still
	// remains available.
	h.Options.Schedule(provider)
	return grokSSOImportWorkerResult{
		created: true,
		item: GrokSSOToOAuthItemResult{
			Index:    index,
			Name:     name,
			Email:    tokenInfo.Email,
			Provider: provider,
		},
	}
}

// GrokSSOImportCredentials combines allowlisted request operations with
// exchanged token fields and returns sanitized credentials. A nonnil built map
// is mutated and returned by MergeCredentials; a nil one is allocated. Callers
// provide writable built storage and stabilize any shared nested request
// values.
//
// Exchanged keys win, including present nil values, while a nonblank requested
// base_url is restored in trimmed form. Sensitive request keys are excluded and
// temporary secret keys are removed from the final map, retaining the three
// accepted exchanged token keys. If built aliases the request map, these
// destination writes are shared; ordinary import construction supplies separate
// maps.
func GrokSSOImportCredentials(built map[string]any, reqCredentials map[string]any) map[string]any {
	// Request operations are inherited separately from exchanged OAuth fields.
	// Filtering top-level keys excludes temporary SSO/password inputs from
	// operational inheritance; nested header or model settings retain their
	// supplied contents.
	allowedReqKeys := map[string]struct{}{
		"base_url":      {},
		"model_mapping": {},

		"header_override":         {},
		"header_overrides":        {},
		"header_override_enabled": {},

		"custom_headers": {},
	}
	ops := map[string]any{}
	for k, v := range reqCredentials {
		if _, ok := allowedReqKeys[k]; !ok {
			continue
		}
		if IsSensitiveCredentialKey(k) {
			continue
		}
		ops[k] = v
	}
	// Merge into the writable exchanged map so returned token keys retain
	// precedence; omitted allowed operations can still borrow their nested
	// request values.
	credentials := MergeCredentials(ops, built)
	// The merged destination can contain keys from older callers. Remove
	// sensitive inputs other than accepted exchanged token fields before storage
	// sanitation runs.
	for k := range credentials {
		if IsSensitiveCredentialKey(k) {
			// Access, refresh, and ID tokens are accepted exchanged secrets. Keep
			// those fields while stripping temporary and unrelated sensitive inputs.
			if k == "access_token" || k == "refresh_token" || k == "id_token" {
				continue
			}
			delete(credentials, k)
		}
	}
	if reqBaseURL, ok := reqCredentials["base_url"].(string); ok && strings.TrimSpace(reqBaseURL) != "" {
		credentials["base_url"] = strings.TrimSpace(reqBaseURL)
	}
	return SanitizeStoredCredentials(PlatformGrok, credentials)
}

// GrokSSOImportExpiry chooses provider expiry settings from request pointers
// and token renewal capability. Nil information, a nonblank refresh token, or
// nonpositive token expiry returns the original pointers.
//
// A positive expiry without renewal creates new result values and forces
// automatic pause. An earlier positive requested expiry caps that token
// deadline; zero, negative, or later requested deadlines do not extend it. This
// selection keeps a token-only provider's configured lifetime within its
// observed credential lifetime while leaving renewable providers under the
// supplied administrator settings.
func GrokSSOImportExpiry(requestExpiresAt *int64, requestAutoPause *bool, tokenInfo *GrokTokenInfo) (*int64, *bool) {
	if tokenInfo == nil || strings.TrimSpace(tokenInfo.RefreshToken) != "" || tokenInfo.ExpiresAt <= 0 {
		return requestExpiresAt, requestAutoPause
	}

	expiresAt := tokenInfo.ExpiresAt
	// An earlier administrator expiry remains effective for token-only
	// creation; later requested lifetime cannot extend this observed credential
	// deadline.
	if requestExpiresAt != nil && *requestExpiresAt > 0 && *requestExpiresAt < expiresAt {
		expiresAt = *requestExpiresAt
	}
	// Token-only authorization cannot renew at expiry. Keep its provider pause
	// enabled even when the supplied request asked to remain schedulable after
	// that credential deadline.
	autoPause := true
	return &expiresAt, &autoPause
}

// CloneGrokSSOMap recursively copies acyclic string-key maps and interface
// slices, preserving nil map input. Other mutable value forms remain shared
// through CloneGrokSSOValue, so callers stabilize those references during
// independent worker use.
func CloneGrokSSOMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = CloneGrokSSOValue(value)
	}
	return clone
}

// CloneGrokSSOValue copies acyclic map[string]any and []any containers
// recursively and returns other values unchanged. Named containers, scalar
// pointers, structs, and other slices therefore retain any mutable sharing
// supplied by their owner.
func CloneGrokSSOValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return CloneGrokSSOMap(v)
	case []any:
		clone := make([]any, len(v))
		for i, item := range v {
			clone[i] = CloneGrokSSOValue(item)
		}
		return clone
	default:
		return value
	}
}

// normalizeSSOImportTokens returns the first occurrence of each normalized SSO
// value in single-input-then-list order. Commas and carriage returns become
// newline separators before each fragment reaches Options.NormalizeToken;
// empty normalized results are skipped.
//
// Normalized text supplies both deduplication identity and the value later
// exchanged by import workers. Splitting before comparison lets pasted CRLF
// and comma lists follow the same ordering rule. Callers provide the normalizer
// and keep input storage stable during traversal; the returned slice contains
// the accepted values in their first-encounter order.
func (h *GrokProviderImport) normalizeSSOImportTokens(tokens []string, single string) []string {
	items := make([]string, 0, len(tokens)+1)
	if strings.TrimSpace(single) != "" {
		items = append(items, single)
	}
	items = append(items, tokens...)
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		// Pasted lists can mix commas and CRLF with newline separators. Split
		// those forms before normalization so deduplication operates on complete
		// token inputs.
		parts := strings.SplitSeq(strings.NewReplacer(",", "\n", "\r", "\n").Replace(item), "\n")
		for token := range parts {
			if token = h.Options.NormalizeToken(token); token == "" {
				continue
			}
			if _, ok := seen[token]; ok {
				continue
			}
			seen[token] = struct{}{}
			result = append(result, token)
		}
	}
	return result
}

// GrokSSOImportProviderName chooses trimmed requested name, then token email,
// then the standard label. Batches append the supplied one-based index when
// total exceeds one so repeated base names remain distinguishable.
func GrokSSOImportProviderName(base string, tokenInfo *GrokTokenInfo, index, total int) string {
	base = strings.TrimSpace(base)
	if base == "" && tokenInfo != nil {
		base = strings.TrimSpace(tokenInfo.Email)
	}
	if base == "" {
		base = "Grok OAuth Provider"
	}
	if total > 1 {
		return base + " #" + strconv.Itoa(index)
	}
	return base
}

// GrokSSOImportErrorMessage formats an application error's public reason and
// message for an import item. An absent mapped status returns empty; callers
// choose error inputs appropriate for the public response.
func GrokSSOImportErrorMessage(err error) string {
	status := infraerrors.FromError(err)
	if status == nil {
		return ""
	}
	if status.Reason != "" {
		return status.Reason + ": " + status.Message
	}
	return status.Message
}

// CreateFromSSO returns ordered success/failure lists for normalized temporary
// SSO inputs. No surviving input returns ErrGrokSSOInput before worker
// creation; owners supply asynchronous RunTask callbacks and stable shared
// request values.
//
// At most three workers write distinct indexed slots, and closing dispatch
// followed by the completion barrier makes those slots readable before
// partitioning. Cancellation is passed to item operations while dispatch still
// visits every normalized token and waits for actual callbacks. A recovered
// panic becomes one failed item, and saved providers or later scheduling
// effects are not rolled back by the batch response.
func (h *GrokProviderImport) CreateFromSSO(ctx context.Context, req GrokSSOToOAuthRequest) (*GrokSSOToOAuthResponse, error) {
	tokens := h.normalizeSSOImportTokens(req.SSOTokens, req.SSOToken)
	if len(tokens) == 0 {
		return nil, ErrGrokSSOInput
	}

	// Assign one result slot per normalized input before launching independent
	// workers; closing jobs and joining them completes all writes before
	// ordered response partitioning.
	workerCount := min(len(tokens), grokSSOImportConcurrency)
	jobs := make(chan grokSSOImportJob)
	items := make([]grokSSOImportWorkerResult, len(tokens))
	var wg sync.WaitGroup
	for range workerCount {
		wg.Add(1)
		h.Options.RunTask("handler/admin/grok_oauth_handler.go:CreateProvidersFromSSO", func() {
			defer wg.Done()
			for job := range jobs {
				items[job.index] = h.safeCreateProviderFromSSOToken(ctx, req, job.token, job.index+1, len(tokens))
			}
		})
	}
	for i, token := range tokens {
		jobs <- grokSSOImportJob{index: i, token: token}
	}
	close(jobs)
	wg.Wait()

	result := GrokSSOToOAuthResponse{
		Created: make([]GrokSSOToOAuthItemResult, 0, len(tokens)),
		Failed:  make([]GrokSSOToOAuthItemResult, 0),
	}
	for _, item := range items {
		if item.created {
			result.Created = append(result.Created, item.item)
		} else {
			result.Failed = append(result.Failed, item.item)
		}
	}
	return &result, nil
}

// CreateFromOAuth completes saved browser authorization and creates an OAuth
// provider using returned credentials. Blank name uses exchanged email and then
// the standard label; quota scheduling follows successful Create and its
// callback needs to return normally.
func (h *GrokProviderImport) CreateFromOAuth(ctx context.Context, req GrokOAuthProviderCreateInput) (*Record, error) {
	tokenInfo, err := h.Authorization.ExchangeCode(ctx, &GrokExchangeCodeInput{
		SessionID: req.SessionID,

		Code: req.Code,

		State: req.State,

		RedirectURI: req.RedirectURI,

		ProxyID: req.ProxyID,
	})
	if err != nil {
		return nil, err
	}
	credentials := h.Authorization.BuildProviderCredentials(tokenInfo)

	name := strings.TrimSpace(req.Name)
	if name == "" && tokenInfo.Email != "" {
		name = tokenInfo.Email
	}
	if name == "" {
		name = "Grok OAuth Provider"
	}

	provider, err := h.Options.Create(ctx, &CreateProviderInput{
		Name: name,

		Platform: PlatformGrok,

		Type: ProviderTypeOAuth,

		Credentials: credentials,

		ProxyID: req.ProxyID,

		Concurrency: req.Concurrency,

		Priority: req.Priority,

		GroupIDs: req.GroupIDs,
	})
	if err != nil {
		return nil, err
	}
	// Provider creation has already been accepted before quota work is
	// scheduled. The callback needs to return normally so that saved record can
	// reach the administrator response.
	h.Options.Schedule(provider)
	return provider, nil
}

// RefreshProvider loads and validates a Grok OAuth record, exchanges its
// renewable credentials, and sends a merged update through Options.Update.
// Successful Get supplies a nonnil row, and exchange/update errors are
// returned.
//
// The exchange builder's token fields replace provided keys while
// MergeCredentials inherits omitted stored metadata by reference. A nonblank
// stored base URL is restored after builder defaults, keeping the selected
// endpoint in this administrator update. Options.Update owns persistence
// comparison and concurrent-edit policy for this supplied credential map.
func (h *GrokProviderImport) RefreshProvider(ctx context.Context, providerID int64) (*Record, error) {
	provider, err := h.Options.Get(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if provider.Platform != PlatformGrok {
		return nil, &GrokImportInputError{Message: "Provider platform does not match Grok OAuth endpoint"}
	}
	if !provider.IsOAuth() {
		return nil, &GrokImportInputError{Message: "Cannot refresh non-OAuth provider credentials"}
	}
	tokenInfo, err := h.Authorization.RefreshProviderToken(ctx, provider)
	if err != nil {
		return nil, err
	}
	newCredentials := h.Authorization.BuildProviderCredentials(tokenInfo)
	// Inherit omitted metadata into exchanged credentials, then retain the
	// operator's endpoint; persistence comparison and concurrent-edit policy
	// remain owned by the supplied Update callback.
	newCredentials = MergeCredentials(provider.Credentials, newCredentials)
	if baseURL := strings.TrimSpace(provider.GetCredential("base_url")); baseURL != "" {
		newCredentials["base_url"] = baseURL
	}
	updatedProvider, err := h.Options.Update(ctx, providerID, &UpdateProviderInput{
		Credentials: newCredentials,
	})
	if err != nil {
		return nil, err
	}
	return updatedProvider, nil
}
