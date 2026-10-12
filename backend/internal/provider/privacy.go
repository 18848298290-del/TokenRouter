package provider

import (
	"context"
	"errors"
	"maps"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/egress"
)

// Privacy maintenance performs an upstream request before conditionally saving
// the observed mode for the same provider identity. When a provider selects a
// proxy, maintenance requires a usable URL for that proxy; failed lookup skips
// the request. Existing modes can short-circuit ensure calls, while force calls
// request a fresh upstream result.
//
// Registered requests share shutdown cancellation, and results returned after
// cancellation are discarded before the conditional write. Identity conflicts
// also discard the result. A write error can still return the observed mode, so
// callers distinguish upstream observation from confirmed persistence. Successful
// Antigravity writes copy the Extra map before applying the mode locally; OpenAI
// ensure and refresh rely on persisted state, while force updates local Extra.

// Privacy operation kinds select existing-mode reuse and observation callbacks.
// Force bypasses mode reuse, while refresh follows the background logging path.
const (
	privacyEnsure  privacyOperation = iota // reuse acceptable recorded modes
	privacyForce                           // request fresh upstream observation
	privacyRefresh                         // background refresh observation handling
)

// ErrPrivacyStopped rejects registration after privacy shutdown begins, allowing
// maintenance callers to skip work once the service has stopped accepting operations.
var ErrPrivacyStopped = errors.New("provider privacy maintenance stopped")

// PrivacyStore persists observed modes only while the provider identity used
// for the upstream request still matches its stored identity.
type PrivacyStore interface {
	// UpdatePrivacyModeIfUnchanged saves the mode for the observed identity and
	// reports whether it applied. Identity conflicts return false without an error;
	// write errors let the service report an upstream result that was not confirmed
	// as persisted. Privacy updates compare identity rather than health windows.
	UpdatePrivacyModeIfUnchanged(context.Context, UsageObservationVersion, string) (bool, error)
}

// PrivacyProxyReader supplies the configured egress proxy for an upstream
// privacy request and lets the service reject unavailable proxy selections.
type PrivacyProxyReader interface {
	// GetByID returns the selected proxy or a lookup error for privacy maintenance.
	// A nil result also means unavailable; the service skips its upstream request
	// when a configured proxy cannot be resolved to a nonempty URL.
	GetByID(context.Context, int64) (*egress.Proxy, error)
}

// PrivacyOptions supplies upstream mode observations and maintenance logging
// callbacks used after lifecycle and configured-proxy eligibility checks.
type PrivacyOptions struct {
	// OpenAI receives the operation context, access token, and resolved proxy URL.
	// Nil disables OpenAI requests; returned modes include upstream failure markers.
	OpenAI func(context.Context, string, string) string
	// Antigravity receives context, access token, project ID, and resolved proxy
	// URL. Nil disables its requests; callbacks should observe shutdown cancellation.
	Antigravity func(context.Context, string, string, string) string
	// AdminObserve receives formatted administrative write failures. The constructor
	// substitutes a no-op when omitted; OpenAI ensure does not report writes here.
	AdminObserve func(string, ...any)
	// Warn reports background write failures and unavailable selected proxy lookups.
	// Info reports accepted background privacy writes with their observed mode.
	// Warn and Info share the constructor default: each nil callback becomes a
	// no-op before maintenance uses either callback, so missing logging does not
	// prevent the selected privacy operation from proceeding.
	Warn, Info func(string, ...any)
}

// PrivacyService coordinates identity-checked privacy writes and cancellation
// for administrative requests and post-refresh observations on OAuth providers.
type PrivacyService struct {
	// activity registers observations before proxy lookup. StopContext cancels
	// registered work and waits for its completion within the caller's budget.
	activity operationActivity // registered request cancellation tracking
	// store must be configured whenever an enabled upstream callback can return
	// a nonempty mode. Conditional writes leave changed provider identities untouched.
	store PrivacyStore
	// proxies is required only for a record with ProxyID. Its absence skips the
	// request for that record instead of selecting an unconfigured direct connection.
	proxies PrivacyProxyReader
	options PrivacyOptions // upstream and observation callback configuration
}

// privacyOperation selects mode reuse, local application, and reporting behavior
// within the shared upstream observation and conditional persistence flow.
type privacyOperation uint8

// NewPrivacyService constructs inactive privacy maintenance with supplied stores
// and callbacks. Missing logging callbacks become no-ops; nil upstream callbacks
// disable their platform requests. A conditional writer is required before an
// enabled callback returns a nonempty mode.
func NewPrivacyService(store PrivacyStore, proxies PrivacyProxyReader, options PrivacyOptions) *PrivacyService {
	noop := func(string, ...any) {}
	if options.AdminObserve == nil {
		options.AdminObserve = noop
	}
	if options.Warn == nil {
		options.Warn = noop
	}
	if options.Info == nil {
		options.Info = noop
	}
	return &PrivacyService{store: store, proxies: proxies, options: options}
}

// ShouldSkipOpenAIPrivacyEnsure reports whether a present stored mode suppresses
// another OpenAI ensure request. Missing keys return false. Present values are
// treated as skippable, including nil, nonstring, empty, and unrecognized values.
// The two trimmed failure markers are the values that permit another request.
func ShouldSkipOpenAIPrivacyEnsure(extra map[string]any) bool {
	if extra == nil {
		return false
	}
	raw, ok := extra["privacy_mode"]
	if !ok {
		return false
	}
	// Stored key presence controls reuse even when its value has the wrong type.
	// Such values become an empty mode, which suppresses another ensure request.
	mode, _ := raw.(string)
	mode = strings.TrimSpace(mode)
	return mode != PrivacyModeFailed && mode != PrivacyModeCFBlocked
}

// ApplyAntigravityPrivacyMode replaces a record's Extra map with a shallow copy
// containing mode as supplied. Nil records and whitespace-only modes are ignored.
// Nested Extra values remain shared; callers synchronize access to the record
// while its map is replaced.
func ApplyAntigravityPrivacyMode(value *Record, mode string) {
	if value == nil || strings.TrimSpace(mode) == "" {
		return
	}
	// Copy the top-level map before replacing privacy_mode so another retained map
	// keeps its own mode entry; nested values continue to reference their existing data.
	extra := make(map[string]any, len(value.Extra)+1)
	maps.Copy(extra, value.Extra)
	extra["privacy_mode"] = mode
	value.Extra = extra
}

// proxyURL returns a privacy request's proxy URL and whether resolution permits
// the request. Service and record must be nonnil. An absent ProxyID allows the
// empty direct URL, while missing readers, failed lookups, nil proxies, and empty
// configured URLs reject the request.
func (s *PrivacyService) proxyURL(ctx context.Context, value *Record) (string, bool) {
	if value.ProxyID == nil {
		return "", true
	}
	if s.proxies == nil {
		return "", false
	}
	// A selected proxy is part of the provider's request identity. Lookup failure
	// must skip the privacy request so maintenance cannot silently connect directly.
	proxy, err := s.proxies.GetByID(ctx, *value.ProxyID)
	if err != nil || proxy == nil {
		s.options.Warn("provider_privacy_proxy_unavailable", "provider_id", value.ID, "proxy_id", *value.ProxyID, "error", err)
		return "", false
	}
	url := proxy.URL()
	return url, url != ""
}

// apply observes and conditionally persists a mode for the selected OAuth
// platform and operation. Nil service or record, ineligible records, disabled
// callbacks, missing tokens, failed proxies, canceled requests, and identity
// conflicts return empty. Callers keep the borrowed record stable during the
// observation and synchronize local Extra mutation. Requests that reach
// registration require a nonnil context; stored-mode reuse precedes registration.
//
// Ensure reuses acceptable modes, and force bypasses them. OpenAI administrative
// calls skip credential shadows; refresh eligibility belongs to its caller.
// Registered work captures identity before the upstream request and checks
// cancellation before writing. A persistence error can return the observed mode;
// local Extra changes follow successful writes, with OpenAI ensure and refresh
// leaving local application to their surrounding maintenance paths.
func (s *PrivacyService) apply(ctx context.Context, value *Record, platform string, operation privacyOperation) string {
	if s == nil || value == nil || value.Platform != platform || value.Type != ProviderTypeOAuth {
		return ""
	}
	if platform == PlatformOpenAI {
		// Credential shadows share their parent's OpenAI account. Administrative
		// maintenance skips them; the refresh caller owns its separate eligibility check.
		if operation != privacyRefresh && value.IsCredentialShadow() {
			return ""
		}
		if s.options.OpenAI == nil {
			return ""
		}
		if operation != privacyForce && ShouldSkipOpenAIPrivacyEnsure(value.Extra) {
			return ""
		}
	} else {
		if s.options.Antigravity == nil {
			return ""
		}
		if operation != privacyForce {
			if existing, ok := value.Extra["privacy_mode"].(string); ok && existing == AntigravityPrivacySet {
				return existing
			}
		}
	}
	token, _ := value.Credentials["access_token"].(string)
	if token == "" {
		return ""
	}
	// Register before proxy lookup and the upstream request so shutdown cancels
	// the complete observation and can wait for deferred completion within its budget.
	ctx, finish, err := s.activity.begin(ctx, ErrPrivacyStopped)
	if err != nil {
		// Canceled parents and stopped registration have no observation to persist.
		// Return empty so maintenance callers can leave the provider's mode untouched.
		return ""
	}
	defer finish()
	observed := ObserveUsageVersion(value)
	proxyURL, ok := s.proxyURL(ctx, value)
	if !ok {
		return ""
	}
	var mode string
	if platform == PlatformOpenAI {
		mode = s.options.OpenAI(ctx, token, proxyURL)
	} else {
		project, _ := value.Credentials["project_id"].(string)
		mode = s.options.Antigravity(ctx, token, project, proxyURL)
	}
	// A callback may ignore cancellation and return late. Discard its mode when
	// cancellation is observed here; the writer receives the same context for persistence.
	if ctx.Err() != nil {
		return ""
	}
	if mode == "" {
		return ""
	}
	// Persist against the identity captured before the request. A changed token,
	// proxy, status, parent, or quota dimension invalidates that observation's write.
	applied, writeErr := s.store.UpdatePrivacyModeIfUnchanged(ctx, observed, mode)
	err = writeErr
	// An identity conflict discards this mode rather than reporting an observation
	// from superseded credentials. Return empty and leave local Extra unchanged.
	if err == nil && !applied {
		return ""
	}
	// Refresh reports persistence separately from the upstream mode. Return the
	// observed string on write errors while applying Antigravity Extra only after success.
	if operation == privacyRefresh {
		failure, success := "token_refresh.update_privacy_mode_failed", "token_refresh.privacy_mode_set"
		if platform == PlatformAntigravity {
			failure = "token_refresh.update_antigravity_privacy_mode_failed"
			success = "token_refresh.antigravity_privacy_mode_set"
		}
		if err != nil {
			s.options.Warn(failure, "provider_id", value.ID, "error", err)
		} else {
			if platform == PlatformAntigravity {
				ApplyAntigravityPrivacyMode(value, mode)
			}
			s.options.Info(success, "provider_id", value.ID, "privacy_mode", mode)
		}
		return mode
	}
	// OpenAI ensure returns its observation after attempting persistence. Keep the
	// borrowed Extra unchanged even when storage accepts the mode or reports an error.
	if platform == PlatformOpenAI && operation == privacyEnsure {
		return mode
	}
	// Administrative write failures retain the upstream observation as the result,
	// while the diagnostic records why that mode was not confirmed in storage.
	if err != nil {
		message := "update_antigravity_privacy_mode_failed: provider_id=%d err=%v"
		if platform == PlatformOpenAI {
			message = "force_update_openai_privacy_mode_failed: provider_id=%d err=%v"
		} else if operation == privacyForce {
			message = "force_update_antigravity_privacy_mode_failed: provider_id=%d err=%v"
		}
		s.options.AdminObserve(message, value.ID, err)
		return mode
	}
	// Local administrative state follows an accepted write. Antigravity replaces
	// Extra's top-level map; OpenAI force edits its existing map under caller synchronization.
	if platform == PlatformAntigravity {
		ApplyAntigravityPrivacyMode(value, mode)
	} else {
		if value.Extra == nil {
			value.Extra = map[string]any{}
		}
		value.Extra["privacy_mode"] = mode
	}
	return mode
}

// EnsureOpenAIPrivacy requests an OpenAI OAuth mode when the stored value needs
// retry. Credential shadows are skipped. The returned observation may survive a
// write error, and this method leaves local Extra application to its caller.
// Requests that reach registration require a nonnil ctx; keep v stable through
// observation and conditional persistence.
func (s *PrivacyService) EnsureOpenAIPrivacy(ctx context.Context, v *Record) string {
	return s.apply(ctx, v, PlatformOpenAI, privacyEnsure)
}

// ForceOpenAIPrivacy requests a fresh OpenAI OAuth mode even when one is stored.
// Credential shadows are skipped. A successful conditional write updates local
// Extra; a write error can return the observed mode without local application.
// Supply a nonnil ctx for registered work and keep v stable during the request.
// Synchronize access to v.Extra when the successful write is applied locally.
func (s *PrivacyService) ForceOpenAIPrivacy(ctx context.Context, v *Record) string {
	return s.apply(ctx, v, PlatformOpenAI, privacyForce)
}

// EnsureAntigravityPrivacy reuses the exact recorded privacy_set mode or requests
// a fresh Antigravity OAuth observation. Reuse can succeed after shutdown because
// it starts no request. A successful conditional write applies the mode through
// a shallow replacement of local Extra. Request registration requires a nonnil
// ctx, and v stays stable during observation; synchronize the local map replacement.
func (s *PrivacyService) EnsureAntigravityPrivacy(ctx context.Context, v *Record) string {
	return s.apply(ctx, v, PlatformAntigravity, privacyEnsure)
}

// ForceAntigravityPrivacy requests a fresh Antigravity OAuth mode regardless of
// the recorded mode. Successful conditional writes shallow-copy local Extra;
// write errors can return the upstream observation while leaving that record
// unchanged. Stopped or canceled observations return empty. Registered work
// requires a nonnil ctx and stable v; synchronize the successful Extra replacement.
func (s *PrivacyService) ForceAntigravityPrivacy(ctx context.Context, v *Record) string {
	return s.apply(ctx, v, PlatformAntigravity, privacyForce)
}

// RefreshOpenAIPrivacy retries eligible OpenAI OAuth privacy observations after
// token refresh and reports write results through background callbacks. The caller
// checks credential-shadow eligibility beforehand. This method discards the
// returned mode and leaves local Extra unchanged. Supply a nonnil ctx when a
// request needs registration and keep v stable until observation and writing finish.
func (s *PrivacyService) RefreshOpenAIPrivacy(ctx context.Context, v *Record) {
	s.apply(ctx, v, PlatformOpenAI, privacyRefresh)
}

// RefreshAntigravityPrivacy retries Antigravity OAuth privacy maintenance when
// privacy_set is absent. Successful conditional writes apply the mode locally;
// failures use background warning callbacks. The shared observation flow discards
// canceled and identity-conflicting results. Registration requires a nonnil ctx;
// keep v stable during the request and synchronize a successful local Extra replacement.
func (s *PrivacyService) RefreshAntigravityPrivacy(ctx context.Context, v *Record) {
	s.apply(ctx, v, PlatformAntigravity, privacyRefresh)
}

// StopContext rejects new request registration, cancels active observations, and
// waits within ctx's budget for a nonnil service. After the first shutdown
// finishes, its result, including timeout, is retained for later calls. A concurrent
// caller can exhaust its own context while waiting for that first result.
// Supply a nonnil ctx while waiting for active work or another shutdown caller.
// EnsureAntigravityPrivacy can still return stored privacy_set without registering work.
func (s *PrivacyService) StopContext(ctx context.Context) error {
	return s.activity.stop(ctx, "provider privacy maintenance")
}
