package gateway

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// Forwarding settings combine exact-string toggles, prompt values, and the
// selected TTFT interpretation into one cached snapshot. Missing fingerprint,
// prompt-injection, and dateline toggles default to enabled; other switches are
// disabled unless their value is exactly "true". Version bounds have a separate
// cache because callers retrieve their minimum and maximum together.
//
// Shared refreshes use bounded reads that retain context values after request
// cancellation. Storage failures temporarily publish default forwarding values
// or empty version bounds and retry after five seconds; successful reads last
// sixty seconds. Administrative publication replaces the relevant cache entries
// separately and forgets shared keys for future work. Already-running refreshes
// can still publish after that replacement, and invalidation likewise expires
// the stored entry without cancelling an older database read.

// Forwarding setting keys and mode values define the persisted controls read
// by protocol adapters and the cache/read timing used to interpret them.
const (
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
	OpenAITTFTModeSemantic                           = "semantic" // semantic event classification mode
	OpenAITTFTModeVisible                            = "visible"  // visible content classification mode

	// versionBoundsCacheTTL retains successfully loaded version bounds until a
	// later gateway read needs to refresh their paired values.
	versionBoundsCacheTTL = 60 * time.Second

	// versionBoundsErrorTTL shortens retry delay after a batch settings-read
	// failure leaves client version checks with an unrestricted range.
	versionBoundsErrorTTL = 5 * time.Second

	// versionBoundsDBTimeout limits the batch database read while the initiating
	// request's cancellation and deadline are detached from shared refresh work.
	versionBoundsDBTimeout = 5 * time.Second

	gatewayForwardingCacheTTL = 60 * time.Second
	gatewayForwardingErrorTTL = 5 * time.Second

	// ForwardingSettingsReadTimeout limits forwarding lookups and the Grok startup
	// migration's shared budget for reading and writing its default model setting.
	ForwardingSettingsReadTimeout = 5 * time.Second
	gatewayForwardingDBTimeout    = ForwardingSettingsReadTimeout
)

// cachedVersionBounds holds a pair of version bounds and its expiration deadline.
type cachedVersionBounds struct {
	// min and max come from the same returned settings map and define the
	// accepted client-version range. An empty value disables its corresponding
	// bound for callers performing version checks.
	min       string // lower client version bound
	max       string // upper client version bound
	expiresAt int64  // unix nano
}

// cachedGatewayForwardingSettings holds interpreted forwarding values with their cache deadline.
type cachedGatewayForwardingSettings struct {
	openAITTFTMode string // stored TTFT mode string
	// provider fingerprint unification toggle
	fingerprintUnification bool
	// client metadata passthrough toggle
	metadataPassthrough bool
	// billing attribution signing toggle
	cchSigning bool
	// Anthropic system injection toggle
	claudeOAuthSystemPromptInjection bool
	// configured Anthropic system text
	claudeOAuthSystemPrompt       string
	claudeOAuthSystemPromptBlocks string // JSON system content blocks
	// one-hour cache injection toggle
	anthropicCacheTTL1hInjection bool
	// message cache-control rewrite toggle
	rewriteMessageCacheControl bool
	// client dateline normalization toggle
	clientDatelineNormalization bool
	expiresAt                   int64 // unix nano
}

// gatewayForwardingSettingsResult is the forwarding value tuple returned from cache reads.
type gatewayForwardingSettingsResult struct {
	openAITTFTMode string // stored TTFT mode string
	// fp selects provider fingerprint unification, mp client metadata passthrough,
	// cch billing attribution signing, claudeOAuthSystemPromptInjection Anthropic
	// system injection, cacheTTL1h one-hour cache injection, and
	// rewriteMessageCacheControl message cache-control rewriting.
	fp, mp, cch, claudeOAuthSystemPromptInjection, cacheTTL1h, rewriteMessageCacheControl bool
	// client dateline normalization toggle
	clientDatelineNormalization bool
	// configured system prompt text and configured JSON content blocks
	claudeOAuthSystemPrompt, claudeOAuthSystemPromptBlocks string
}

// ForwardingSnapshot holds persisted settings to publish to the local cache.
type ForwardingSnapshot struct {
	// OpenAITTFTMode is stored as supplied by PublishForwarding. The TTFT getter
	// normalizes this string when choosing semantic or visible event classification.
	OpenAITTFTMode string
	// provider fingerprint unification toggle
	FingerprintUnification bool
	// client metadata passthrough toggle
	MetadataPassthrough bool
	// billing attribution signing toggle
	CCHSigning bool
	// Anthropic system injection toggle
	ClaudeOAuthSystemPromptInjection bool
	// configured Anthropic system text
	ClaudeOAuthSystemPrompt       string
	ClaudeOAuthSystemPromptBlocks string // JSON system content blocks
	// one-hour cache injection toggle
	AnthropicCacheTTL1hInjection bool
	// message cache-control rewrite toggle
	RewriteMessageCacheControl bool
	// client dateline normalization toggle
	ClientDatelineNormalization bool
}

// GetGrokDefaultBaseURLMode reads and normalizes the persisted Grok endpoint
// mode using an independent five-second budget. A nil receiver, missing
// repository, read error, or unknown value returns cli; a read requires a nonnil context.
func (s *RuntimeSettings) GetGrokDefaultBaseURLMode(ctx context.Context) string {
	if s == nil || s.settingRepo == nil {
		return "cli"
	}
	// Endpoint mode has its own read deadline. A cancelled request can still
	// obtain the configured mode before its caller resolves the provider base URL.
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gatewayForwardingDBTimeout)
	defer cancel()
	raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyGrokDefaultBaseURLMode)
	if err != nil {
		// Endpoint selection keeps its CLI default when the mode cannot be read,
		// allowing the caller to resolve a supported base URL during settings failure.
		return "cli"
	}
	return NormalizeGrokDefaultBaseURLMode(raw)
}

// NormalizeOpenAITTFTMode returns visible for its trimmed, case-insensitive
// spelling and semantic for all other inputs. This reader default is permissive;
// administration preparation separately rejects unknown nonblank mode values.
func NormalizeOpenAITTFTMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), OpenAITTFTModeVisible) {
		return OpenAITTFTModeVisible
	}
	return OpenAITTFTModeSemantic
}

// getGatewayForwardingSettingsCached returns interpreted forwarding values from
// the current unexpired cache or a shared refresh. Supply a nonnil runtime,
// repository, and context; refreshes have a five-second budget detached from
// request cancellation while retaining its values.
//
// A successful batch read interprets absent and empty switches using their
// individual defaults, then publishes one snapshot for sixty seconds. A failed
// read publishes semantic TTFT, enabled fingerprint/prompt/dateline switches,
// and otherwise zero values for five seconds. This distinguishes an unavailable
// settings store from a successfully read explicit false toggle; future callers
// recheck the store after the shorter error deadline.
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
		// Coalesced callers need the refresh after the initiating request cancels.
		// An independent read budget bounds the shared database operation while
		// retaining that request's context values.
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
			// A failed batch read provides one default snapshot to its waiting
			// callers. The shorter deadline permits another lookup soon after storage
			// recovers instead of retaining these defaults for the normal lifetime.
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
		// Interpret every field before replacing the cache with this batch's tuple
		// and deadline, so one accessor call cannot combine different refresh results.
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
	// An unexpected shared value uses the same interpreted defaults as storage
	// failure, so forwarding accessors receive a defined tuple rather than
	// unresolved switch values.
	return gatewayForwardingSettingsResult{openAITTFTMode: OpenAITTFTModeSemantic, fp: true, claudeOAuthSystemPromptInjection: true, clientDatelineNormalization: true}
}

// GetOpenAITTFTMode returns the normalized time-to-first-token interpretation
// from the forwarding cache. Missing or failed reads use semantic mode, and
// expired entries use the shared refresh rules documented by
// getGatewayForwardingSettingsCached.
func (s *RuntimeSettings) GetOpenAITTFTMode(ctx context.Context) string {
	return NormalizeOpenAITTFTMode(s.getGatewayForwardingSettingsCached(ctx).openAITTFTMode)
}

// GetGatewayForwardingSettings returns fingerprint, metadata passthrough, and CCH signing switches.
// The three values come from one interpreted forwarding snapshot. Missing or
// failed reads enable fingerprint unification and disable the two other switches;
// expiration triggers the shared refresh.
func (s *RuntimeSettings) GetGatewayForwardingSettings(ctx context.Context) (fingerprintUnification, metadataPassthrough, cchSigning bool) {
	result := s.getGatewayForwardingSettingsCached(ctx)
	return result.fp, result.mp, result.cch
}

// IsAnthropicCacheTTL1hInjectionEnabled reports whether to inject the one-hour Anthropic cache TTL.
// Missing or failed forwarding reads return false. The value comes from the
// shared snapshot, whose expiration and refresh rules apply to this accessor.
func (s *RuntimeSettings) IsAnthropicCacheTTL1hInjectionEnabled(ctx context.Context) bool {
	return s.getGatewayForwardingSettingsCached(ctx).cacheTTL1h
}

// IsRewriteMessageCacheControlEnabled reports whether to rewrite message cache_control.
// Missing or failed forwarding reads return false. This accessor uses the shared
// interpreted snapshot, with refresh and default selection delegated to
// getGatewayForwardingSettingsCached.
func (s *RuntimeSettings) IsRewriteMessageCacheControlEnabled(ctx context.Context) bool {
	return s.getGatewayForwardingSettingsCached(ctx).rewriteMessageCacheControl
}

// IsClientDatelineNormalizationEnabled reports whether to normalize client datelines.
// Missing, empty, or failed forwarding reads return true. Explicit nonempty
// values enable normalization only for the literal "true", using the shared
// forwarding cache's refresh rules.
func (s *RuntimeSettings) IsClientDatelineNormalizationEnabled(ctx context.Context) bool {
	return s.getGatewayForwardingSettingsCached(ctx).clientDatelineNormalization
}

// GetClaudeOAuthSystemPromptInjectionSettings returns the injection switch, text, and content blocks.
// All three values come from one forwarding snapshot. Missing or failed reads
// enable injection with empty prompt values; configured text and JSON block
// strings are retained for the Anthropic injector to interpret.
func (s *RuntimeSettings) GetClaudeOAuthSystemPromptInjectionSettings(ctx context.Context) (enabled bool, prompt string, blocks string) {
	result := s.getGatewayForwardingSettingsCached(ctx)
	return result.claudeOAuthSystemPromptInjection, result.claudeOAuthSystemPrompt, result.claudeOAuthSystemPromptBlocks
}

// GetClaudeCodeVersionBounds returns the configured minimum and maximum from an
// unexpired snapshot or shared refresh. Supply a nonnil runtime, repository,
// and context; empty bounds disable the corresponding client range check.
//
// Concurrent expired-cache callers share a read that retains context values
// after request cancellation and has its own five-second budget. Successful
// reads cache the returned bounds for sixty seconds. Storage failure publishes
// empty bounds for five seconds, letting requests continue through range checking
// while allowing a later request to retry the settings read.
func (s *RuntimeSettings) GetClaudeCodeVersionBounds(ctx context.Context) (min, max string) {
	if cached, ok := s.versionBoundsCache.Load().(*cachedVersionBounds); ok {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.min, cached.max
		}
	}
	// bounds carries the paired version values returned by a shared refresh.
	type bounds struct {
		// min and max come from the same cache or batch read. Empty values leave
		// their corresponding client range limits unset when callers run validation.
		min, max string // lower version bound; upper version bound
	}
	result, err, _ := s.versionBoundsSF.Do("version_bounds", func() (any, error) {
		// A preceding query or administration publication may populate the cache
		// before this callback starts. Reuse that valid pair before issuing another read.
		if cached, ok := s.versionBoundsCache.Load().(*cachedVersionBounds); ok {
			if time.Now().UnixNano() < cached.expiresAt {
				return bounds{cached.min, cached.max}, nil
			}
		}
		// Keep the shared read alive after client cancellation so its result can
		// populate the version cache for other callers, with an independent timeout
		// bounding the database operation.
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), versionBoundsDBTimeout)
		defer cancel()
		values, err := s.settingRepo.GetMultiple(dbCtx, []string{
			SettingKeyMinClaudeCodeVersion,
			SettingKeyMaxClaudeCodeVersion,
		})
		if err != nil {
			// Empty bounds disable range restrictions after a settings read fails.
			// A short expiration keeps that permissive result available briefly while
			// allowing storage recovery to be observed on the next refresh.
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
		// Cache both bounds with this read's deadline in one replacement, allowing
		// later range checks to use the returned pair as a complete snapshot.
		s.versionBoundsCache.Store(&cachedVersionBounds{
			min:       b.min,
			max:       b.max,
			expiresAt: time.Now().Add(versionBoundsCacheTTL).UnixNano(),
		})
		return b, nil
	})
	if err != nil {
		// A failed shared result cannot supply usable range data. Empty bounds
		// let the version-check caller use its documented unrestricted range result.
		return "", ""
	}
	b, ok := result.(bounds)
	if !ok {
		// Only the paired bounds result can populate range checks. An unexpected
		// shared value returns empty bounds so callers have a defined default.
		return "", ""
	}
	return b.min, b.max
}

// PublishForwarding installs persisted version bounds and a forwarding snapshot
// with fresh sixty-second deadlines. TTFT mode is stored as supplied and
// normalized by its getter. The two caches are replaced separately, and an
// already-running refresh can still store its result after a shared key is
// forgotten.
//
// Each replacement publishes one complete cache object. Callers needing both
// caches from one revision coordinate those reads separately, because this
// method supplies neither a combined cache object nor a revision check across
// the two replacements.
func (s *RuntimeSettings) PublishForwarding(minVersion, maxVersion string, value ForwardingSnapshot) {
	// Forget shared keys for future reads, then install the persisted values.
	// Existing callbacks continue independently, and version bounds and forwarding
	// values each have their own cache replacement.
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

// InvalidateForwarding expires this instance's forwarding snapshot so a later
// reader queries the store. It forgets future shared work but leaves an already-running
// refresh able to store its result; the separate version-bounds cache is unaffected.
func (s *RuntimeSettings) InvalidateForwarding() {
	// Store an expired zero snapshot to force a later reader through refresh.
	// Forgetting the key permits new shared work without cancelling callbacks
	// already reading a prior settings value.
	s.gatewayForwardingSF.Forget("gateway_forwarding")
	s.gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
}

// IsIdentityPatchEnabled reads the switch per request and returns true on read failure.
// A successful read enables it only for the literal "true", including false
// for an empty value. The caller supplies the request context directly to
// storage, and this setting is not cached.
func (s *RuntimeSettings) IsIdentityPatchEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyEnableIdentityPatch)
	if err != nil {
		// A missing or failed read keeps identity patching enabled for this
		// request, while a successful empty value remains the explicit disabled result.
		return true
	}
	return value == "true"
}

// GetIdentityPatchPrompt reads the identity patch prompt and returns empty on read failure.
// A successful read returns the stored text unchanged. It uses the request's
// context directly and performs a new storage lookup on every call.
func (s *RuntimeSettings) GetIdentityPatchPrompt(ctx context.Context) string {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyIdentityPatchPrompt)
	if err != nil {
		// An unavailable custom prompt supplies no replacement text. Returning
		// empty leaves the downstream identity patcher responsible for its default prompt.
		return ""
	}
	return value
}
