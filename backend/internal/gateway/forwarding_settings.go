package gateway

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

const (
	// Persistent forwarding-setting keys and TTFT mode values.
	SettingKeyClaudeOAuthSystemPrompt                = "claude_oauth_system_prompt"
	SettingKeyClaudeOAuthSystemPromptBlocks          = "claude_oauth_system_prompt_blocks"
	SettingKeyEnableAnthropicCacheTTL1hInjection     = "enable_anthropic_cache_ttl_1h_injection"
	SettingKeyEnableCCHSigning                       = "enable_cch_signing"
	SettingKeyEnableClaudeOAuthSystemPromptInjection = "enable_claude_oauth_system_prompt_injection"
	SettingKeyEnableClientDatelineNormalization      = "enable_client_dateline_normalization"
	SettingKeyEnableFingerprintUnification           = "enable_fingerprint_unification"
	SettingKeyEnableMetadataPassthrough              = "enable_metadata_passthrough"
	SettingKeyMaxClaudeCodeVersion                   = "max_claude_code_version"
	SettingKeyMinClaudeCodeVersion                   = "min_claude_code_version"
	SettingKeyOpenAITTFTMode                         = "openai_ttft_mode"
	SettingKeyRewriteMessageCacheControl             = "rewrite_message_cache_control"
	OpenAITTFTModeSemantic                           = "semantic"
	OpenAITTFTModeVisible                            = "visible"

	// versionBoundsCacheTTL is the lifetime of successfully loaded version bounds.
	versionBoundsCacheTTL = 60 * time.Second

	// versionBoundsErrorTTL allows an early retry after a database error.
	versionBoundsErrorTTL = 5 * time.Second

	// versionBoundsDBTimeout bounds the shared database query independently of request cancellation.
	versionBoundsDBTimeout = 5 * time.Second

	gatewayForwardingCacheTTL = 60 * time.Second
	gatewayForwardingErrorTTL = 5 * time.Second

	// ForwardingSettingsReadTimeout bounds reads from the forwarding-settings store.
	ForwardingSettingsReadTimeout = 5 * time.Second
	gatewayForwardingDBTimeout    = ForwardingSettingsReadTimeout
)

// cachedVersionBounds holds process-local Claude Code version bounds for 60 seconds.
type cachedVersionBounds struct {
	min       string // Empty disables the lower bound.
	max       string // Empty disables the upper bound.
	expiresAt int64  // unix nano
}

// cachedGatewayForwardingSettings holds process-local forwarding settings for 60 seconds.
type cachedGatewayForwardingSettings struct {
	openAITTFTMode                   string
	fingerprintUnification           bool
	metadataPassthrough              bool
	cchSigning                       bool
	claudeOAuthSystemPromptInjection bool
	claudeOAuthSystemPrompt          string
	claudeOAuthSystemPromptBlocks    string
	anthropicCacheTTL1hInjection     bool
	rewriteMessageCacheControl       bool
	clientDatelineNormalization      bool
	expiresAt                        int64 // unix nano
}

type gatewayForwardingSettingsResult struct {
	openAITTFTMode                                                                        string
	fp, mp, cch, claudeOAuthSystemPromptInjection, cacheTTL1h, rewriteMessageCacheControl bool
	clientDatelineNormalization                                                           bool
	claudeOAuthSystemPrompt, claudeOAuthSystemPromptBlocks                                string
}

// ForwardingSnapshot holds persisted settings to publish to the local cache.
type ForwardingSnapshot struct {
	OpenAITTFTMode                   string
	FingerprintUnification           bool
	MetadataPassthrough              bool
	CCHSigning                       bool
	ClaudeOAuthSystemPromptInjection bool
	ClaudeOAuthSystemPrompt          string
	ClaudeOAuthSystemPromptBlocks    string
	AnthropicCacheTTL1hInjection     bool
	RewriteMessageCacheControl       bool
	ClientDatelineNormalization      bool
}

// GetGrokDefaultBaseURLMode reads the Grok base URL mode within a timeout, defaulting to CLI.
func (s *RuntimeSettings) GetGrokDefaultBaseURLMode(ctx context.Context) string {
	if s == nil || s.settingRepo == nil {
		return "cli"
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gatewayForwardingDBTimeout)
	defer cancel()
	raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyGrokDefaultBaseURLMode)
	if err != nil {
		return "cli"
	}
	return NormalizeGrokDefaultBaseURLMode(raw)
}

// NormalizeOpenAITTFTMode accepts visible and defaults other values to semantic.
func NormalizeOpenAITTFTMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), OpenAITTFTModeVisible) {
		return OpenAITTFTModeVisible
	}
	return OpenAITTFTModeSemantic
}

// getGatewayForwardingSettingsCached coalesces reads after the instance's cache expires.
func (s *RuntimeSettings) getGatewayForwardingSettingsCached(ctx context.Context) gatewayForwardingSettingsResult {
	if cached, ok := s.gatewayForwardingCache.Load().(*cachedGatewayForwardingSettings); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return gatewayForwardingSettingsResult{
				openAITTFTMode:                   cached.openAITTFTMode,
				fp:                               cached.fingerprintUnification,
				mp:                               cached.metadataPassthrough,
				cch:                              cached.cchSigning,
				claudeOAuthSystemPromptInjection: cached.claudeOAuthSystemPromptInjection,
				claudeOAuthSystemPrompt:          cached.claudeOAuthSystemPrompt,
				claudeOAuthSystemPromptBlocks:    cached.claudeOAuthSystemPromptBlocks,
				cacheTTL1h:                       cached.anthropicCacheTTL1hInjection,
				rewriteMessageCacheControl:       cached.rewriteMessageCacheControl,
				clientDatelineNormalization:      cached.clientDatelineNormalization,
			}
		}
	}
	val, _, _ := s.gatewayForwardingSF.Do("gateway_forwarding", func() (any, error) {
		if cached, ok := s.gatewayForwardingCache.Load().(*cachedGatewayForwardingSettings); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return gatewayForwardingSettingsResult{
					openAITTFTMode:                   cached.openAITTFTMode,
					fp:                               cached.fingerprintUnification,
					mp:                               cached.metadataPassthrough,
					cch:                              cached.cchSigning,
					claudeOAuthSystemPromptInjection: cached.claudeOAuthSystemPromptInjection,
					claudeOAuthSystemPrompt:          cached.claudeOAuthSystemPrompt,
					claudeOAuthSystemPromptBlocks:    cached.claudeOAuthSystemPromptBlocks,
					cacheTTL1h:                       cached.anthropicCacheTTL1hInjection,
					rewriteMessageCacheControl:       cached.rewriteMessageCacheControl,
					clientDatelineNormalization:      cached.clientDatelineNormalization,
				}, nil
			}
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gatewayForwardingDBTimeout)
		defer cancel()
		values, err := s.settingRepo.GetMultiple(dbCtx, []string{
			SettingKeyOpenAITTFTMode,
			SettingKeyEnableFingerprintUnification,
			SettingKeyEnableMetadataPassthrough,
			SettingKeyEnableCCHSigning,
			SettingKeyEnableClaudeOAuthSystemPromptInjection,
			SettingKeyClaudeOAuthSystemPrompt,
			SettingKeyClaudeOAuthSystemPromptBlocks,
			SettingKeyEnableAnthropicCacheTTL1hInjection,
			SettingKeyRewriteMessageCacheControl,
			SettingKeyEnableClientDatelineNormalization,
		})
		if err != nil {
			slog.Warn("failed to get gateway forwarding settings", "error", err)
			s.gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{
				openAITTFTMode:                   OpenAITTFTModeSemantic,
				fingerprintUnification:           true,
				metadataPassthrough:              false,
				cchSigning:                       false,
				claudeOAuthSystemPromptInjection: true,
				anthropicCacheTTL1hInjection:     false,
				rewriteMessageCacheControl:       false,
				clientDatelineNormalization:      true,
				expiresAt:                        time.Now().Add(gatewayForwardingErrorTTL).UnixNano(),
			})
			return gatewayForwardingSettingsResult{
				openAITTFTMode:                   OpenAITTFTModeSemantic,
				fp:                               true,
				claudeOAuthSystemPromptInjection: true,
				rewriteMessageCacheControl:       false,
				clientDatelineNormalization:      true,
			}, nil
		}
		ttftMode := NormalizeOpenAITTFTMode(values[SettingKeyOpenAITTFTMode])
		fp := true
		if v, ok := values[SettingKeyEnableFingerprintUnification]; ok && v != "" {
			fp = v == "true"
		}
		mp := values[SettingKeyEnableMetadataPassthrough] == "true"
		cch := values[SettingKeyEnableCCHSigning] == "true"
		claudeOAuthSystemPromptInjection := true
		if v, ok := values[SettingKeyEnableClaudeOAuthSystemPromptInjection]; ok && v != "" {
			claudeOAuthSystemPromptInjection = v == "true"
		}
		claudeOAuthSystemPrompt := values[SettingKeyClaudeOAuthSystemPrompt]
		claudeOAuthSystemPromptBlocks := values[SettingKeyClaudeOAuthSystemPromptBlocks]
		cacheTTL1h := values[SettingKeyEnableAnthropicCacheTTL1hInjection] == "true"
		rewriteMessageCacheControl := false
		if v, ok := values[SettingKeyRewriteMessageCacheControl]; ok && v != "" {
			rewriteMessageCacheControl = v == "true"
		}
		clientDatelineNormalization := true
		if v, ok := values[SettingKeyEnableClientDatelineNormalization]; ok && v != "" {
			clientDatelineNormalization = v == "true"
		}
		s.gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{
			openAITTFTMode:                   ttftMode,
			fingerprintUnification:           fp,
			metadataPassthrough:              mp,
			cchSigning:                       cch,
			claudeOAuthSystemPromptInjection: claudeOAuthSystemPromptInjection,
			claudeOAuthSystemPrompt:          claudeOAuthSystemPrompt,
			claudeOAuthSystemPromptBlocks:    claudeOAuthSystemPromptBlocks,
			anthropicCacheTTL1hInjection:     cacheTTL1h,
			rewriteMessageCacheControl:       rewriteMessageCacheControl,
			clientDatelineNormalization:      clientDatelineNormalization,
			expiresAt:                        time.Now().Add(gatewayForwardingCacheTTL).UnixNano(),
		})
		return gatewayForwardingSettingsResult{
			openAITTFTMode:                   ttftMode,
			fp:                               fp,
			mp:                               mp,
			cch:                              cch,
			claudeOAuthSystemPromptInjection: claudeOAuthSystemPromptInjection,
			claudeOAuthSystemPrompt:          claudeOAuthSystemPrompt,
			claudeOAuthSystemPromptBlocks:    claudeOAuthSystemPromptBlocks,
			cacheTTL1h:                       cacheTTL1h,
			rewriteMessageCacheControl:       rewriteMessageCacheControl,
			clientDatelineNormalization:      clientDatelineNormalization,
		}, nil
	})
	if r, ok := val.(gatewayForwardingSettingsResult); ok {
		return r
	}
	return gatewayForwardingSettingsResult{openAITTFTMode: OpenAITTFTModeSemantic, fp: true, claudeOAuthSystemPromptInjection: true, clientDatelineNormalization: true}
}

// GetOpenAITTFTMode returns the normalized time-to-first-token mode from cached settings.
func (s *RuntimeSettings) GetOpenAITTFTMode(ctx context.Context) string {
	return NormalizeOpenAITTFTMode(s.getGatewayForwardingSettingsCached(ctx).openAITTFTMode)
}

// GetGatewayForwardingSettings returns fingerprint, metadata passthrough, and CCH signing switches.
func (s *RuntimeSettings) GetGatewayForwardingSettings(ctx context.Context) (fingerprintUnification, metadataPassthrough, cchSigning bool) {
	result := s.getGatewayForwardingSettingsCached(ctx)
	return result.fp, result.mp, result.cch
}

// IsAnthropicCacheTTL1hInjectionEnabled reports whether to inject the one-hour Anthropic cache TTL.
func (s *RuntimeSettings) IsAnthropicCacheTTL1hInjectionEnabled(ctx context.Context) bool {
	return s.getGatewayForwardingSettingsCached(ctx).cacheTTL1h
}

// IsRewriteMessageCacheControlEnabled reports whether to rewrite message cache_control.
func (s *RuntimeSettings) IsRewriteMessageCacheControlEnabled(ctx context.Context) bool {
	return s.getGatewayForwardingSettingsCached(ctx).rewriteMessageCacheControl
}

// IsClientDatelineNormalizationEnabled reports whether to normalize client datelines.
func (s *RuntimeSettings) IsClientDatelineNormalizationEnabled(ctx context.Context) bool {
	return s.getGatewayForwardingSettingsCached(ctx).clientDatelineNormalization
}

// GetClaudeOAuthSystemPromptInjectionSettings returns the injection switch, text, and content blocks.
func (s *RuntimeSettings) GetClaudeOAuthSystemPromptInjectionSettings(ctx context.Context) (enabled bool, prompt string, blocks string) {
	result := s.getGatewayForwardingSettingsCached(ctx)
	return result.claudeOAuthSystemPromptInjection, result.claudeOAuthSystemPrompt, result.claudeOAuthSystemPromptBlocks
}

// GetClaudeCodeVersionBounds reads cached version bounds and refreshes them after expiration.
func (s *RuntimeSettings) GetClaudeCodeVersionBounds(ctx context.Context) (min, max string) {
	if cached, ok := s.versionBoundsCache.Load().(*cachedVersionBounds); ok {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.min, cached.max
		}
	}
	// Concurrent callers share one database query.
	type bounds struct{ min, max string }
	result, err, _ := s.versionBoundsSF.Do("version_bounds", func() (any, error) {
		// A preceding query may have populated the cache before this callback started.
		if cached, ok := s.versionBoundsCache.Load().(*cachedVersionBounds); ok {
			if time.Now().UnixNano() < cached.expiresAt {
				return bounds{cached.min, cached.max}, nil
			}
		}
		// Keep the shared read alive after client cancellation so it can populate the cache.
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), versionBoundsDBTimeout)
		defer cancel()
		values, err := s.settingRepo.GetMultiple(dbCtx, []string{
			SettingKeyMinClaudeCodeVersion,
			SettingKeyMaxClaudeCodeVersion,
		})
		if err != nil {
			// Allow requests on database failure and use a short TTL before retrying the read.
			slog.Warn("failed to get claude code version bounds setting, skipping version check", "error", err)
			s.versionBoundsCache.Store(&cachedVersionBounds{
				min:       "",
				max:       "",
				expiresAt: time.Now().Add(versionBoundsErrorTTL).UnixNano(),
			})
			return bounds{"", ""}, nil
		}
		b := bounds{
			min: values[SettingKeyMinClaudeCodeVersion],
			max: values[SettingKeyMaxClaudeCodeVersion],
		}
		s.versionBoundsCache.Store(&cachedVersionBounds{
			min:       b.min,
			max:       b.max,
			expiresAt: time.Now().Add(versionBoundsCacheTTL).UnixNano(),
		})
		return b, nil
	})
	if err != nil {
		return "", ""
	}
	b, ok := result.(bounds)
	if !ok {
		return "", ""
	}
	return b.min, b.max
}

// PublishForwarding refreshes the version and forwarding caches with persisted values and fresh TTLs.
func (s *RuntimeSettings) PublishForwarding(minVersion, maxVersion string, value ForwardingSnapshot) {
	s.versionBoundsSF.Forget("version_bounds")
	s.versionBoundsCache.Store(&cachedVersionBounds{min: minVersion, max: maxVersion, expiresAt: time.Now().Add(versionBoundsCacheTTL).UnixNano()})
	s.gatewayForwardingSF.Forget("gateway_forwarding")
	s.gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{
		openAITTFTMode:                   value.OpenAITTFTMode,
		fingerprintUnification:           value.FingerprintUnification,
		metadataPassthrough:              value.MetadataPassthrough,
		cchSigning:                       value.CCHSigning,
		claudeOAuthSystemPromptInjection: value.ClaudeOAuthSystemPromptInjection,
		claudeOAuthSystemPrompt:          value.ClaudeOAuthSystemPrompt,
		claudeOAuthSystemPromptBlocks:    value.ClaudeOAuthSystemPromptBlocks,
		anthropicCacheTTL1hInjection:     value.AnthropicCacheTTL1hInjection,
		rewriteMessageCacheControl:       value.RewriteMessageCacheControl,
		clientDatelineNormalization:      value.ClientDatelineNormalization,
		expiresAt:                        time.Now().Add(gatewayForwardingCacheTTL).UnixNano(),
	})
}

// InvalidateForwarding expires this instance's cache so the next read queries the store.
func (s *RuntimeSettings) InvalidateForwarding() {
	s.gatewayForwardingSF.Forget("gateway_forwarding")
	s.gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
}

// IsIdentityPatchEnabled reads the switch per request and returns true on read failure.
func (s *RuntimeSettings) IsIdentityPatchEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyEnableIdentityPatch)
	if err != nil {
		return true
	}
	return value == "true"
}

// GetIdentityPatchPrompt reads the identity patch prompt and returns empty on read failure.
func (s *RuntimeSettings) GetIdentityPatchPrompt(ctx context.Context) string {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyIdentityPatchPrompt)
	if err != nil {
		return ""
	}
	return value
}
