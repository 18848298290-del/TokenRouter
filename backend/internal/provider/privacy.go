package provider

import (
	"context"
	"errors"
	"maps"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/egress"
)

const (
	privacyEnsure privacyOperation = iota
	privacyForce
	privacyRefresh
)

var ErrPrivacyStopped = errors.New("provider privacy maintenance stopped")

type PrivacyStore interface {
	UpdatePrivacyModeIfUnchanged(context.Context, UsageObservationVersion, string) (bool, error)
}
type PrivacyProxyReader interface {
	GetByID(context.Context, int64) (*egress.Proxy, error)
}

// PrivacyOptions supplies platform privacy requests and observation callbacks.
type PrivacyOptions struct {
	OpenAI       func(context.Context, string, string) string
	Antigravity  func(context.Context, string, string, string) string
	AdminObserve func(string, ...any)
	Warn, Info   func(string, ...any)
}
type PrivacyService struct {
	activity operationActivity
	store    PrivacyStore
	proxies  PrivacyProxyReader
	options  PrivacyOptions
}

type privacyOperation uint8

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

func ShouldSkipOpenAIPrivacyEnsure(extra map[string]any) bool {
	if extra == nil {
		return false
	}
	raw, ok := extra["privacy_mode"]
	if !ok {
		return false
	}
	mode, _ := raw.(string)
	mode = strings.TrimSpace(mode)
	return mode != PrivacyModeFailed && mode != PrivacyModeCFBlocked
}

func ApplyAntigravityPrivacyMode(value *Record, mode string) {
	if value == nil || strings.TrimSpace(mode) == "" {
		return
	}
	extra := make(map[string]any, len(value.Extra)+1)
	maps.Copy(extra, value.Extra)
	extra["privacy_mode"] = mode
	value.Extra = extra
}

// proxyURL distinguishes direct access from an unavailable configured proxy, which must not trigger direct
// access.
func (s *PrivacyService) proxyURL(ctx context.Context, value *Record) (string, bool) {
	if value.ProxyID == nil {
		return "", true
	}
	if s.proxies == nil {
		return "", false
	}
	proxy, err := s.proxies.GetByID(ctx, *value.ProxyID)
	if err != nil || proxy == nil {
		s.options.Warn("provider_privacy_proxy_unavailable", "provider_id", value.ID, "proxy_id", *value.ProxyID, "error", err)
		return "", false
	}
	url := proxy.URL()
	return url, url != ""
}

func (s *PrivacyService) apply(ctx context.Context, value *Record, platform string, operation privacyOperation) string {
	if s == nil || value == nil || value.Platform != platform || value.Type != ProviderTypeOAuth {
		return ""
	}
	if platform == PlatformOpenAI {
		// Administrative operations reject shadows; the refresh caller checks eligibility before this path.
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
	ctx, finish, err := s.activity.begin(ctx, ErrPrivacyStopped)
	if err != nil {
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
	if ctx.Err() != nil {
		return ""
	}
	if mode == "" {
		return ""
	}
	applied, writeErr := s.store.UpdatePrivacyModeIfUnchanged(ctx, observed, mode)
	err = writeErr
	if err == nil && !applied {
		return ""
	}
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
	if platform == PlatformOpenAI && operation == privacyEnsure {
		return mode
	}
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

func (s *PrivacyService) EnsureOpenAIPrivacy(ctx context.Context, v *Record) string {
	return s.apply(ctx, v, PlatformOpenAI, privacyEnsure)
}

func (s *PrivacyService) ForceOpenAIPrivacy(ctx context.Context, v *Record) string {
	return s.apply(ctx, v, PlatformOpenAI, privacyForce)
}

func (s *PrivacyService) EnsureAntigravityPrivacy(ctx context.Context, v *Record) string {
	return s.apply(ctx, v, PlatformAntigravity, privacyEnsure)
}

func (s *PrivacyService) ForceAntigravityPrivacy(ctx context.Context, v *Record) string {
	return s.apply(ctx, v, PlatformAntigravity, privacyForce)
}

func (s *PrivacyService) RefreshOpenAIPrivacy(ctx context.Context, v *Record) {
	s.apply(ctx, v, PlatformOpenAI, privacyRefresh)
}

func (s *PrivacyService) RefreshAntigravityPrivacy(ctx context.Context, v *Record) {
	s.apply(ctx, v, PlatformAntigravity, privacyRefresh)
}

// StopContext cancels and waits for privacy operations. Later privacy calls return empty results.
func (s *PrivacyService) StopContext(ctx context.Context) error {
	return s.activity.stop(ctx, "provider privacy maintenance")
}
