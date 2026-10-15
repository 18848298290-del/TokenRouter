package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// client_settings.go reads and caches outbound client identities and the Claude
// Codex plugin permission. Antigravity versions pass through an
// application-supplied normalizer and dynamic default; Codex User-Agent
// overrides are trimmed and use a fixed default when absent. The Claude Codex
// plugin permission is a separate exact-string toggle with a disabled default.
//
// Each value has its own shared refresh and expiration deadline. Refresh reads
// detach request cancellation while retaining context values and use bounded
// database timeouts. Read errors store defaults for a short retry interval;
// successful or missing settings use the normal interval. Administrative
// publication forgets shared work for future reads and replaces the cache, but
// an already-running refresh can still finish and store its result afterward.

// Client-setting cache durations govern successful reuse, failure retry timing,
// and independent database-read budgets for outbound identity and plugin settings.
const (
	antigravityUserAgentVersionCacheTTL  = 60 * time.Second
	antigravityUserAgentVersionErrorTTL  = 5 * time.Second
	antigravityUserAgentVersionDBTimeout = 5 * time.Second
	openAICodexUserAgentCacheTTL         = 60 * time.Second
	openAICodexUserAgentErrorTTL         = 5 * time.Second
	openAICodexUserAgentDBTimeout        = 5 * time.Second
	// DefaultOpenAICodexUserAgent supplies the fixed outbound Codex identity used
	// when no usable persisted override can be read or published.
	DefaultOpenAICodexUserAgent     = "codex-tui/0.144.1 (Ubuntu 22.4.0; x86_64) xterm-256color (codex-tui; 0.144.1)"
	openAIAllowCodexPluginCacheTTL  = 60 * time.Second
	openAIAllowCodexPluginErrorTTL  = 5 * time.Second
	openAIAllowCodexPluginDBTimeout = 5 * time.Second
)

// ClientSettingsOptions supplies platform normalization callbacks and dynamic version defaults.
type ClientSettingsOptions struct {
	// NormalizeUserAgentVersion interprets persisted and published Antigravity
	// overrides. Supply it before those operations; an empty result selects the default.
	NormalizeUserAgentVersion func(string) string
	// DefaultUserAgentVersion supplies the current platform default before every
	// Antigravity read, including cache hits, and when publication receives an unusable override.
	DefaultUserAgentVersion func() string
}

// cachedAntigravityUserAgentVersion is a version value with its cache-expiration deadline.
type cachedAntigravityUserAgentVersion struct {
	version   string // normalized or default version
	expiresAt int64  // Unix nanoseconds
}

// cachedOpenAICodexUserAgent is an outbound header value with its expiration deadline.
type cachedOpenAICodexUserAgent struct {
	value     string // selected outbound User-Agent
	expiresAt int64  // Unix nanoseconds
}

// cachedOpenAIAllowCodexPlugin is a plugin permission with its expiration deadline.
type cachedOpenAIAllowCodexPlugin struct {
	value     bool  // interpreted plugin permission
	expiresAt int64 // Unix nanoseconds
}

// GetAntigravityUserAgentVersion returns a cached normalized override or the
// dynamic platform default. Supply a nonnil RuntimeSettings and usable client
// callbacks, since the default callback runs before the repository guard or
// cache lookup. A nil repository returns that default immediately.
//
// A nil context becomes Background. Expired-cache callers share a read with a
// five-second budget independent of request cancellation; storage failures cache
// the default for five seconds, and successful or missing values are normalized
// and cached for sixty seconds.
func (s *RuntimeSettings) GetAntigravityUserAgentVersion(ctx context.Context) string {
	fallback := s.clientOptions.DefaultUserAgentVersion()
	if s == nil || s.settingRepo == nil {
		return fallback
	}
	if cached, ok := s.antigravityUAVersionCache.Load().(*cachedAntigravityUserAgentVersion); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.version
		}
	}

	result, _, _ := s.antigravityUAVersionSF.Do("antigravity_user_agent_version", func() (any, error) {
		if cached, ok := s.antigravityUAVersionCache.Load().(*cachedAntigravityUserAgentVersion); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.version, nil
			}
		}
		if ctx == nil {
			ctx = context.Background()
		}
		// This refresh serves other waiting callers. Retain context values while
		// replacing request cancellation with a bounded budget for the shared read.
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), antigravityUserAgentVersionDBTimeout)
		defer cancel()
		value, err := s.settingRepo.GetValue(dbCtx, SettingKeyAntigravityUserAgentVersion)
		if err != nil && !errors.Is(err, s.notFound) {
			// A failed override read uses the dynamic default so identity resolution
			// can continue, with a short expiration that permits an early storage retry.
			slog.Warn("failed to get antigravity user agent version setting", "error", err)
			s.antigravityUAVersionCache.Store(&cachedAntigravityUserAgentVersion{
				version:   fallback,
				expiresAt: time.Now().Add(antigravityUserAgentVersionErrorTTL).UnixNano(),
			})
			return fallback, nil
		}
		version := s.clientOptions.NormalizeUserAgentVersion(value)
		if version == "" {
			version = fallback
		}
		// Store the version with this lookup's normal deadline in one replacement,
		// so cache hits cannot pair the selected identity with another refresh's expiry.
		s.antigravityUAVersionCache.Store(&cachedAntigravityUserAgentVersion{
			version:   version,
			expiresAt: time.Now().Add(antigravityUserAgentVersionCacheTTL).UnixNano(),
		})
		return version, nil
	})
	if version, ok := result.(string); ok && version != "" {
		return version
	}
	return fallback
}

// GetOpenAICodexUserAgent returns the trimmed persisted Codex override or the
// fixed default header. A nil receiver or repository uses the default, and a
// nil context becomes Background when a refresh is needed.
//
// Expired-cache callers share a five-second read independent of request
// cancellation. Read failures store the default for five seconds; successful,
// empty, and missing settings use the normal sixty-second cache lifetime.
func (s *RuntimeSettings) GetOpenAICodexUserAgent(ctx context.Context) string {
	fallback := DefaultOpenAICodexUserAgent
	if s == nil || s.settingRepo == nil {
		return fallback
	}
	if cached, ok := s.openAICodexUACache.Load().(*cachedOpenAICodexUserAgent); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.value
		}
	}

	result, _, _ := s.openAICodexUASF.Do("openai_codex_user_agent", func() (any, error) {
		if cached, ok := s.openAICodexUACache.Load().(*cachedOpenAICodexUserAgent); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.value, nil
			}
		}
		if ctx == nil {
			ctx = context.Background()
		}
		// Coalesced identity reads need to outlive the initiating request's
		// cancellation. The independent timeout bounds the database operation for
		// all callers waiting on this refresh.
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAICodexUserAgentDBTimeout)
		defer cancel()
		value, err := s.settingRepo.GetValue(dbCtx, SettingKeyOpenAICodexUserAgent)
		if err != nil && !errors.Is(err, s.notFound) {
			// Use the fixed outbound identity after an override read fails. Its
			// shorter cache lifetime lets restored settings storage be checked sooner.
			slog.Warn("failed to get openai codex user agent setting", "error", err)
			s.openAICodexUACache.Store(&cachedOpenAICodexUserAgent{
				value:     fallback,
				expiresAt: time.Now().Add(openAICodexUserAgentErrorTTL).UnixNano(),
			})
			return fallback, nil
		}
		ua := strings.TrimSpace(value)
		if ua == "" {
			ua = fallback
		}
		// Publish the chosen header and deadline together. A cache hit then reads
		// one complete identity snapshot even when administration publication overlaps this refresh.
		s.openAICodexUACache.Store(&cachedOpenAICodexUserAgent{
			value:     ua,
			expiresAt: time.Now().Add(openAICodexUserAgentCacheTTL).UnixNano(),
		})
		return ua, nil
	})
	if ua, ok := result.(string); ok && ua != "" {
		return ua
	}
	return fallback
}

// IsOpenAIAllowClaudeCodeCodexPluginEnabled reports whether the persisted plugin
// setting is exactly "true". Supply a nonnil receiver, repository, and context;
// expired-cache callers share a bounded read detached from request cancellation.
// Missing values remain disabled for sixty seconds, and read failures are
// disabled for five seconds before another refresh can be attempted.
func (s *RuntimeSettings) IsOpenAIAllowClaudeCodeCodexPluginEnabled(ctx context.Context) bool {
	if cached, ok := s.openAIAllowCodexPluginCache.Load().(*cachedOpenAIAllowCodexPlugin); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.value
		}
	}
	result, _, _ := s.openAIAllowCodexPluginSF.Do("openai_allow_codex_plugin_enabled", func() (any, error) {
		if cached, ok := s.openAIAllowCodexPluginCache.Load().(*cachedOpenAIAllowCodexPlugin); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.value, nil
			}
		}
		// A shared permission read serves requests beyond the initiating caller.
		// Replace its cancellation with a bounded database budget so one cancelled
		// request does not decide the refresh for the others.
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAIAllowCodexPluginDBTimeout)
		defer cancel()
		value, err := s.settingRepo.GetValue(dbCtx, SettingKeyOpenAIAllowClaudeCodeCodexPlugin)
		if err != nil {
			if errors.Is(err, s.notFound) {
				// An absent opt-in leaves plugin access disabled. Cache that configured
				// default for the normal interval because storage returned a known missing key.
				s.openAIAllowCodexPluginCache.Store(&cachedOpenAIAllowCodexPlugin{
					value:     false,
					expiresAt: time.Now().Add(openAIAllowCodexPluginCacheTTL).UnixNano(),
				})
				return false, nil
			}
			slog.Warn("failed to get openai_allow_claude_code_codex_plugin setting", "error", err)
			// Storage failure cannot establish plugin opt-in, so return disabled.
			// A short lifetime lets a later request recheck storage after recovery.
			s.openAIAllowCodexPluginCache.Store(&cachedOpenAIAllowCodexPlugin{
				value:     false,
				expiresAt: time.Now().Add(openAIAllowCodexPluginErrorTTL).UnixNano(),
			})
			return false, nil
		}
		enabled := value == "true"
		// A successful lookup uses the normal lifetime even when permission is
		// disabled. Replace value and expiry together so readers use that lookup's deadline.
		s.openAIAllowCodexPluginCache.Store(&cachedOpenAIAllowCodexPlugin{
			value:     enabled,
			expiresAt: time.Now().Add(openAIAllowCodexPluginCacheTTL).UnixNano(),
		})
		return enabled, nil
	})
	if val, ok := result.(bool); ok {
		return val
	}
	return false
}

// PublishClientUserAgents installs normalized Antigravity and trimmed Codex
// identities after settings persistence. Unusable values select their respective
// defaults and both entries receive sixty-second deadlines. Forgetting shared
// refresh keys affects future reads; already-running callbacks can still store
// their results after this publication.
func (s *RuntimeSettings) PublishClientUserAgents(version, codex string) {
	// Forgetting the key permits a future read to start independently of older
	// shared work. Cache replacement publishes the prepared identity for readers
	// that encounter this snapshot before another refresh stores its result.
	s.antigravityUAVersionSF.Forget("antigravity_user_agent_version")
	antigravityUserAgentVersion := s.clientOptions.NormalizeUserAgentVersion(version)
	if antigravityUserAgentVersion == "" {
		antigravityUserAgentVersion = s.clientOptions.DefaultUserAgentVersion()
	}
	s.antigravityUAVersionCache.Store(&cachedAntigravityUserAgentVersion{
		version:   antigravityUserAgentVersion,
		expiresAt: time.Now().Add(antigravityUserAgentVersionCacheTTL).UnixNano(),
	})
	s.openAICodexUASF.Forget("openai_codex_user_agent")
	codexUA := strings.TrimSpace(codex)
	if codexUA == "" {
		codexUA = DefaultOpenAICodexUserAgent
	}
	s.openAICodexUACache.Store(&cachedOpenAICodexUserAgent{
		value:     codexUA,
		expiresAt: time.Now().Add(openAICodexUserAgentCacheTTL).UnixNano(),
	})
}

// PublishCodexPlugin installs the merged plugin permission after persistence
// succeeds and starts a sixty-second cache lifetime. It forgets the shared-read
// key for future callers while an already-running read can still complete and
// replace the stored value.
func (s *RuntimeSettings) PublishCodexPlugin(enabled bool) {
	// Forget the refresh key before publishing the merged permission so a later
	// cache miss can start a new read independently of callbacks already in progress.
	s.openAIAllowCodexPluginSF.Forget("openai_allow_codex_plugin_enabled")
	s.openAIAllowCodexPluginCache.Store(&cachedOpenAIAllowCodexPlugin{
		value:     enabled,
		expiresAt: time.Now().Add(openAIAllowCodexPluginCacheTTL).UnixNano(),
	})
}

// MigrateGrokDefaultTextModel replaces a stored grok-4.5 default with grok-4.6.
// Missing repositories, absent settings, and other model values produce no
// write. A nil context becomes Background, and the read and conditional write
// share a bounded budget detached from caller cancellation.
func (s *RuntimeSettings) MigrateGrokDefaultTextModel(ctx context.Context) error {
	if s == nil || s.settingRepo == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Migration reads and its eligible replacement write share one bounded
	// operation budget, allowing this startup setting upgrade to finish after
	// cancellation of the context that initiated it.
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ForwardingSettingsReadTimeout)
	defer cancel()

	value, err := s.settingRepo.GetValue(dbCtx, SettingKeyGrokDefaultTextModel)
	if err != nil {
		if errors.Is(err, s.notFound) {
			// An absent default needs no migration. Leave it unconfigured so the
			// normal platform default remains responsible for later model selection.
			return nil
		}
		return fmt.Errorf("get %s setting: %w", SettingKeyGrokDefaultTextModel, err)
	}
	if strings.TrimSpace(value) != "grok-4.5" {
		return nil
	}
	if err := s.settingRepo.Set(dbCtx, SettingKeyGrokDefaultTextModel, "grok-4.6"); err != nil {
		return fmt.Errorf("set %s setting: %w", SettingKeyGrokDefaultTextModel, err)
	}
	return nil
}
