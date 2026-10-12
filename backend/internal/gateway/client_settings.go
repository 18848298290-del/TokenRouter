package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const (
	antigravityUserAgentVersionCacheTTL  = 60 * time.Second
	antigravityUserAgentVersionErrorTTL  = 5 * time.Second
	antigravityUserAgentVersionDBTimeout = 5 * time.Second
	openAICodexUserAgentCacheTTL         = 60 * time.Second
	openAICodexUserAgentErrorTTL         = 5 * time.Second
	openAICodexUserAgentDBTimeout        = 5 * time.Second
	DefaultOpenAICodexUserAgent          = "codex-tui/0.144.1 (Ubuntu 22.4.0; x86_64) xterm-256color (codex-tui; 0.144.1)"
	openAIAllowCodexPluginCacheTTL       = 60 * time.Second
	openAIAllowCodexPluginErrorTTL       = 5 * time.Second
	openAIAllowCodexPluginDBTimeout      = 5 * time.Second
)

// ClientSettingsOptions supplies platform normalization and dynamic defaults.
type ClientSettingsOptions struct {
	NormalizeUserAgentVersion func(string) string
	DefaultUserAgentVersion   func() string
}

type cachedAntigravityUserAgentVersion struct {
	version   string
	expiresAt int64 // Unix nanoseconds
}

type cachedOpenAICodexUserAgent struct {
	value     string
	expiresAt int64 // Unix nanoseconds
}

type cachedOpenAIAllowCodexPlugin struct {
	value     bool
	expiresAt int64 // Unix nanoseconds
}

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
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), antigravityUserAgentVersionDBTimeout)
		defer cancel()
		value, err := s.settingRepo.GetValue(dbCtx, SettingKeyAntigravityUserAgentVersion)
		if err != nil && !errors.Is(err, s.notFound) {
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
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAICodexUserAgentDBTimeout)
		defer cancel()
		value, err := s.settingRepo.GetValue(dbCtx, SettingKeyOpenAICodexUserAgent)
		if err != nil && !errors.Is(err, s.notFound) {
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
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAIAllowCodexPluginDBTimeout)
		defer cancel()
		value, err := s.settingRepo.GetValue(dbCtx, SettingKeyOpenAIAllowClaudeCodeCodexPlugin)
		if err != nil {
			if errors.Is(err, s.notFound) {
				// A missing setting defaults to disabled for the normal cache TTL.
				s.openAIAllowCodexPluginCache.Store(&cachedOpenAIAllowCodexPlugin{
					value:     false,
					expiresAt: time.Now().Add(openAIAllowCodexPluginCacheTTL).UnixNano(),
				})
				return false, nil
			}
			slog.Warn("failed to get openai_allow_claude_code_codex_plugin setting", "error", err)
			// Read failures default to disabled with a short TTL for retry.
			s.openAIAllowCodexPluginCache.Store(&cachedOpenAIAllowCodexPlugin{
				value:     false,
				expiresAt: time.Now().Add(openAIAllowCodexPluginErrorTTL).UnixNano(),
			})
			return false, nil
		}
		enabled := value == "true"
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

// PublishClientUserAgents updates normalized client user agents and their cache expiry.
func (s *RuntimeSettings) PublishClientUserAgents(version, codex string) {
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

// PublishCodexPlugin publishes the merged setting after persistence succeeds.
func (s *RuntimeSettings) PublishCodexPlugin(enabled bool) {
	s.openAIAllowCodexPluginSF.Forget("openai_allow_codex_plugin_enabled")
	s.openAIAllowCodexPluginCache.Store(&cachedOpenAIAllowCodexPlugin{
		value:     enabled,
		expiresAt: time.Now().Add(openAIAllowCodexPluginCacheTTL).UnixNano(),
	})
}

// MigrateGrokDefaultTextModel upgrades the persisted grok-4.5 default to grok-4.6.
func (s *RuntimeSettings) MigrateGrokDefaultTextModel(ctx context.Context) error {
	if s == nil || s.settingRepo == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ForwardingSettingsReadTimeout)
	defer cancel()

	value, err := s.settingRepo.GetValue(dbCtx, SettingKeyGrokDefaultTextModel)
	if err != nil {
		if errors.Is(err, s.notFound) {
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
