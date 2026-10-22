package provider

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strconv"
	"strings"
	"time"
)

// codex_import_rules.go parses Codex import payloads and resolves credential
// identity, expiry, and merge rules for each entry.
// Codex import accepts raw token lines, JSON objects, nested arrays, and
// consecutive JSON values while preserving entry order. Normalization keeps
// explicit account hints ahead of decoded JWT metadata, ignores browser
// session tokens as renewal credentials, and selects signing-mode imports
// separately from OAuth token input.
//
// Expiry handling separates provider scheduling lifetime from access-token
// lifetime. Access-only input must have usable expiry and forces automatic
// pause, while renewable input keeps provider expiry independent; Agent
// Identity bypasses OAuth expiry. JWT payloads are hints without signature
// verification, and undecodable access tokens remain importable with warnings
// until later expiry requirements are checked. Matching keys likewise depend
// on credential mode so access-only sessions use fingerprints and signing
// accounts use account identity. Map merging copies supported values, with
// restored client metadata retaining its supplied object when renewal is
// preserved.

// ParseCodexSessionImportEntries parses Content before Contents and returns
// sequential one-based entries. Blank content sources are omitted, and any
// source parse error aborts with no partial entry result.
//
// Each parser can yield flattened arrays or mixed raw/JSON lines, so indexes
// count produced values across all sources rather than physical line
// positions. The resulting raw objects remain available to
// NormalizeCodexImportEntry for per-entry validation. Empty input returns no
// entries with nil error, leaving the caller to decide whether a batch should
// be submitted.
func ParseCodexSessionImportEntries(req CodexSessionImportRequest) ([]CodexImportEntry, error) {
	contents := make([]string, 0, 1+len(req.Contents))
	if strings.TrimSpace(req.Content) != "" {
		contents = append(contents, req.Content)
	}
	for _, content := range req.Contents {
		if strings.TrimSpace(content) != "" {
			contents = append(contents, content)
		}
	}

	var entries []CodexImportEntry
	for _, content := range contents {
		values, err := parseCodexSessionImportContent(content)
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			entries = append(entries, CodexImportEntry{
				Index: len(entries) + 1,
				Value: value,
			})
		}
	}
	return entries, nil
}

// parseCodexSessionImportContent parses trimmed content as a JSON stream or
// raw token lines. Empty input returns nil success; leading object/array
// syntax chooses JSON decoding first.
//
// If that decode fails and newlines exist, a successful line parse recovers
// mixed JSON and token input. Failed recovery retains the original JSON error,
// while valid JSON values are recursively flattened in source order. This
// distinction accepts browser objects beside raw credentials without treating
// every raw token as JSON or returning partially decoded stream data.
func parseCodexSessionImportContent(content string) ([]any, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil, nil
	}

	if looksLikeJSON(trimmed) {
		values, err := decodeCodexJSONStream(trimmed)
		if err != nil {
			if strings.Contains(trimmed, "\n") {
				// Mixed object/token input can fail whole-stream decoding yet parse
				// per line. Retain successful recovery; failed recovery leaves the
				// original stream diagnostic for the caller.
				if lineValues, lineErr := parseCodexSessionImportLines(trimmed); lineErr == nil {
					return lineValues, nil
				}
			}
			return nil, fmt.Errorf("failed to parse JSON: %w", err)
		}
		return flattenCodexImportValues(values), nil
	}

	return parseCodexSessionImportLines(trimmed)
}

// parseCodexSessionImportLines preserves nonblank line order, decoding
// object/array-looking lines and retaining other text as raw tokens. Errors
// abort the whole parse; their displayed number counts produced values plus
// one rather than physical source line.
func parseCodexSessionImportLines(content string) ([]any, error) {
	values := make([]any, 0)
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if looksLikeJSON(line) {
			lineValues, err := decodeCodexJSONStream(line)
			if err != nil {
				return nil, fmt.Errorf("failed to parse JSON on line %d: %w", len(values)+1, err)
			}
			values = append(values, flattenCodexImportValues(lineValues)...)
			continue
		}
		values = append(values, line)
	}
	return values, nil
}

// decodeCodexJSONStream decodes consecutive JSON values with json.Number
// preservation. It requires at least one value, returns no partial result on
// decode failure, and stops only when the decoder reports clean EOF.
func decodeCodexJSONStream(content string) ([]any, error) {
	decoder := json.NewDecoder(strings.NewReader(content))
	// Keep JSON numbers as their source text so scalar identity and timestamp
	// readers can choose integer or float conversion rather than losing that
	// distinction at the initial decoding step.
	decoder.UseNumber()
	values := make([]any, 0, 1)
	for {
		var value any
		err := decoder.Decode(&value)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if len(values) == 0 {
		return nil, errors.New("empty JSON content")
	}
	return values, nil
}

// flattenCodexImportValues recursively expands arrays into a new outer slice
// in leaf order. Leaf maps and other values remain borrowed; callers provide
// stable acyclic arrays, as the JSON decoder does.
func flattenCodexImportValues(values []any) []any {
	out := make([]any, 0, len(values))
	var appendValue func(any)
	appendValue = func(value any) {
		if arr, ok := value.([]any); ok {
			for _, item := range arr {
				appendValue(item)
			}
			return
		}
		out = append(out, value)
	}
	for _, value := range values {
		appendValue(value)
	}
	return out
}

// NormalizeCodexImportEntry builds credentials, identity hints, expiry, and
// warnings from a stable raw token or object. Nil clock selects time.Now, and
// unsupported input or missing OAuth access token fails.
//
// An agent_identity object or matching auth mode selects signing credentials
// and requires runtime, key, account, user, and configured key validation.
// OAuth objects prefer nested token spellings and explicit account hints;
// ID-token hints fill blanks before access-token hints, while positive
// access-token expiry overrides a previously parsed explicit expiry. JWT
// payloads are unauthenticated metadata and undecodable tokens add warnings.
// Browser session tokens are ignored as renewal material. The returned
// credential mode selects its own ordered matching keys and provider name.
func NormalizeCodexImportEntry(entry CodexImportEntry, options CodexImportOptions) (*CodexImportProvider, error) {
	nowFunc := options.Now
	if nowFunc == nil {
		nowFunc = time.Now
	}
	now := nowFunc().UTC()
	item := &CodexImportProvider{
		Credentials: map[string]any{},
		Extra: map[string]any{
			"import_source": "codex_session",
			"imported_at":   now.Format(time.RFC3339),
		},
	}

	switch raw := entry.Value.(type) {
	case string:
		item.AccessToken = strings.TrimSpace(raw)
	case map[string]any:
		if agentIdentity, ok := firstCodexMap(raw, []string{"agent_identity"}, []string{"agentIdentity"}); ok || strings.EqualFold(firstCodexString(raw, []string{"auth_mode"}, []string{"authMode"}), OpenAIAuthModeAgentIdentity) {
			if !ok {
				agentIdentity = raw
			}
			item.IsAgentIdentity = true
			item.AgentRuntimeID = firstCodexString(agentIdentity, []string{"agent_runtime_id"}, []string{"agentRuntimeId"})
			item.AgentPrivateKey = firstCodexString(agentIdentity, []string{"agent_private_key"}, []string{"agentPrivateKey"})
			item.AgentTaskID = firstCodexString(agentIdentity, []string{"task_id"}, []string{"taskId"})
			item.ProviderID = firstCodexString(agentIdentity, []string{"account_id"}, []string{"accountId"})
			item.UserID = firstCodexString(agentIdentity, []string{"chatgpt_user_id"}, []string{"chatgptUserId"})
			item.Email = firstCodexString(agentIdentity, []string{"email"})
			item.PlanType = firstCodexString(agentIdentity, []string{"plan_type"}, []string{"planType"})
			item.AgentFedRAMP = firstCodexBool(agentIdentity, []string{"chatgpt_account_is_fedramp"}, []string{"chatgptAccountIsFedramp"})
			if item.AgentRuntimeID == "" || item.AgentPrivateKey == "" || item.ProviderID == "" || item.UserID == "" {
				return nil, errors.New("agent identity is missing required fields")
			}
			// Replace validator-specific diagnostics with the fixed import field
			// failure. The enclosing batch reports this item failure and can continue
			// processing other source entries.
			if err := validateCodexPrivateKey(options.ValidatePrivateKey, item.AgentPrivateKey); err != nil {
				return nil, errors.New("invalid agent identity private key format")
			}
			item.Credentials["auth_mode"] = OpenAIAuthModeAgentIdentity
			item.Credentials["agent_runtime_id"] = item.AgentRuntimeID
			item.Credentials["agent_private_key"] = item.AgentPrivateKey
			item.Credentials["chatgpt_account_id"] = item.ProviderID
			item.Credentials["chatgpt_user_id"] = item.UserID
			item.Credentials["chatgpt_account_is_fedramp"] = item.AgentFedRAMP
			setCodexCredentialIfNotEmpty(item.Credentials, "task_id", item.AgentTaskID)
			setCodexCredentialIfNotEmpty(item.Credentials, "email", item.Email)
			setCodexCredentialIfNotEmpty(item.Credentials, "plan_type", item.PlanType)
			if item.AgentTaskID == "" {
				item.WarningTexts = append(item.WarningTexts, "task_id is missing; the first request will register a new task using the existing runtime")
			}
			item.IdentityKeys = BuildCodexAgentIdentityKeys(item.ProviderID)
			item.Name = buildCodexImportProviderName(item, entry.Index)
			return item, nil
		}
		item.AccessToken = firstCodexString(raw,
			[]string{"tokens", "access_token"},
			[]string{"tokens", "accessToken"},
			[]string{"access_token"},
			[]string{"accessToken"},
			[]string{"token"},
		)
		item.RefreshToken = firstCodexString(raw,
			[]string{"tokens", "refresh_token"},
			[]string{"tokens", "refreshToken"},
			[]string{"refresh_token"},
			[]string{"refreshToken"},
		)
		item.IDToken = firstCodexString(raw,
			[]string{"tokens", "id_token"},
			[]string{"tokens", "idToken"},
			[]string{"id_token"},
			[]string{"idToken"},
		)
		item.Email = firstCodexString(raw, []string{"email"}, []string{"user", "email"})
		item.ProviderID = firstCodexString(raw,
			[]string{"chatgpt_account_id"},
			[]string{"chatgptAccountId"},
			[]string{"account_id"},
			[]string{"accountId"},
			[]string{"account", "id"},
			[]string{"account", "account_id"},
			[]string{"account", "chatgpt_account_id"},
		)
		item.UserID = firstCodexString(raw,
			[]string{"chatgpt_user_id"},
			[]string{"chatgptUserId"},
			[]string{"user_id"},
			[]string{"userId"},
			[]string{"user", "id"},
		)
		item.PlanType = firstCodexString(raw,
			[]string{"plan_type"},
			[]string{"planType"},
			[]string{"account", "plan_type"},
			[]string{"account", "planType"},
		)
		item.Organization = firstCodexString(raw,
			[]string{"organization_id"},
			[]string{"organizationId"},
			[]string{"org_id"},
			[]string{"orgId"},
		)
		item.Name = firstCodexString(raw, []string{"name"}, []string{"user", "name"})
		authProvider := firstCodexString(raw, []string{"auth_provider"}, []string{"authProvider"})
		if authProvider != "" {
			item.Extra["auth_provider"] = authProvider
		}
		// Browser session token text is only a presence hint here. OAuth renewal
		// requires the separately parsed refresh credential, so storing that cookie
		// as renewal material would misrepresent this import mode.
		if sessionToken := firstCodexString(raw, []string{"session_token"}, []string{"sessionToken"}); sessionToken != "" {
			item.Extra["session_token_present"] = true
			item.WarningTexts = append(item.WarningTexts, "sessionToken was ignored and will not be stored as an OAuth refresh_token")
		}
		if sessionExpiresAt, ok := codexTimeAt(raw, []string{"expires"}); ok {
			// Store session display time at second precision,
			// separately from token expiry.
			item.Extra["session_expires_at"] = sessionExpiresAt.Format(time.RFC3339)
		}
		// Explicit token expiry receives the inclusive grace cutoff before JWT
		// decoding. A later positive access-token exp can replace this selected
		// timestamp after applying its own strict comparison.
		if tokenExpiresAt, ok := firstCodexTime(raw,
			[]string{"tokens", "expires_at"},
			[]string{"tokens", "expiresAt"},
			[]string{"expires_at"},
			[]string{"expiresAt"},
		); ok {
			if tokenExpiresAt.Unix() <= now.Unix()-codexImportClockSkewSeconds {
				return nil, fmt.Errorf("access_token has expired: %s", tokenExpiresAt.Format(time.RFC3339))
			}
			item.TokenExpiresAt = &tokenExpiresAt
			item.Credentials["expires_at"] = tokenExpiresAt.Format(time.RFC3339)
		}
		copyCodexExtraString(raw, item.Extra, "user_image", []string{"user", "image"})
		copyCodexExtraString(raw, item.Extra, "user_picture", []string{"user", "picture"})
		copyCodexExtraString(raw, item.Extra, "account_structure", []string{"account", "structure"})
		copyCodexExtraString(raw, item.Extra, "account_residency_region", []string{"account", "residencyRegion"})
		copyCodexExtraString(raw, item.Extra, "compute_residency", []string{"account", "computeResidency"})
	default:
		return nil, fmt.Errorf("unsupported format for entry %d", entry.Index)
	}

	if item.IsAgentIdentity {
		return item, nil
	}
	if item.AccessToken == "" {
		return nil, errors.New("missing accessToken/access_token")
	}
	item.Credentials["access_token"] = item.AccessToken
	if item.RefreshToken != "" {
		item.Credentials["refresh_token"] = item.RefreshToken
		item.Credentials["client_id"] = options.OAuthClientID
	}
	if item.IDToken != "" {
		item.Credentials["id_token"] = item.IDToken
		// ID-token metadata fills blank identity first without expiry checks.
		// Ignore its decode failure before access-token hints fill the remaining
		// gaps, keeping explicit object values authoritative.
		_ = enrichCodexImportProviderFromJWT(item, item.IDToken, false, now)
	}
	if err := enrichCodexImportProviderFromJWT(item, item.AccessToken, true, now); err != nil {
		return nil, err
	}
	if _, ok := item.Credentials["expires_at"]; !ok {
		item.WarningTexts = append(item.WarningTexts, "Could not parse accessToken expiry; verify token validity after importing")
	}
	if item.RefreshToken == "" {
		item.WarningTexts = append(item.WarningTexts, "refresh_token is missing; automatic renewal is unavailable after accessToken expires")
	}

	setCodexCredentialIfNotEmpty(item.Credentials, "email", item.Email)
	setCodexCredentialIfNotEmpty(item.Credentials, "chatgpt_account_id", item.ProviderID)
	setCodexCredentialIfNotEmpty(item.Credentials, "chatgpt_user_id", item.UserID)
	setCodexCredentialIfNotEmpty(item.Credentials, "organization_id", item.Organization)
	setCodexCredentialIfNotEmpty(item.Credentials, "plan_type", item.PlanType)

	fingerprint := CodexTokenFingerprint(item.AccessToken)
	item.Extra["access_token_sha256"] = fingerprint
	item.IdentityKeys = BuildCodexImportIdentityKeys(item.ProviderID, item.UserID, item.Email, item.AccessToken, item.RefreshToken)
	item.Name = buildCodexImportProviderName(item, entry.Index)

	return item, nil
}

// enrichCodexImportProviderFromJWT fills blank hints on a nonnil item with
// initialized credentials. Undecodable JWT returns nil error and adds a
// warning only for access-token validation.
//
// With validation enabled, positive exp receives the strict two-minute grace
// check and replaces token/credential expiry. Existing explicit identity wins;
// otherwise account, user, plan, and organization claims fill gaps, with
// subject as final user fallback. Organization POID precedes a default entry,
// then the first entry. Header and signature are not checked, so callers use
// these values as import hints and keep the item exclusively mutable.
func enrichCodexImportProviderFromJWT(item *CodexImportProvider, token string, validateExpiry bool, now time.Time) error {
	// Unreadable JWT metadata leaves explicit token/object values available.
	// Access-token validation emits a warning instead of rejecting opaque token
	// text, with access-only expiry requirements checked by the later caller.
	claims, err := decodeCodexJWTClaims(token)
	if err != nil {
		if validateExpiry {
			item.WarningTexts = append(item.WarningTexts, "accessToken is not a parseable JWT; expiry and provider identity cannot be verified")
		}
		return nil
	}
	// Access-token expiry validates the decoded positive exp with a strict
	// grace comparison. Equality is still accepted here, unlike the later
	// access-only provider expiry check that rejects its inclusive boundary.
	if validateExpiry && claims.Exp > 0 {
		if now.Unix() > claims.Exp+codexImportClockSkewSeconds {
			return fmt.Errorf("access_token has expired: %s", time.Unix(claims.Exp, 0).UTC().Format(time.RFC3339))
		}
		expiresAt := time.Unix(claims.Exp, 0).UTC()
		item.TokenExpiresAt = &expiresAt
		item.Credentials["expires_at"] = expiresAt.Format(time.RFC3339)
	}
	if item.Email == "" {
		item.Email = strings.TrimSpace(claims.Email)
	}
	if claims.OpenAIAuth == nil {
		if item.UserID == "" {
			item.UserID = strings.TrimSpace(claims.Sub)
		}
		return nil
	}
	if item.ProviderID == "" {
		item.ProviderID = strings.TrimSpace(claims.OpenAIAuth.ChatGPTAccountID)
	}
	if item.UserID == "" {
		item.UserID = strings.TrimSpace(claims.OpenAIAuth.ChatGPTUserID)
	}
	if item.UserID == "" {
		item.UserID = strings.TrimSpace(claims.OpenAIAuth.UserID)
	}
	if item.PlanType == "" {
		item.PlanType = strings.TrimSpace(claims.OpenAIAuth.ChatGPTPlanType)
	}
	if item.Organization == "" {
		item.Organization = strings.TrimSpace(claims.OpenAIAuth.POID)
	}
	if item.Organization == "" {
		for _, org := range claims.OpenAIAuth.Organizations {
			if org.IsDefault {
				item.Organization = org.ID
				break
			}
		}
	}
	if item.Organization == "" && len(claims.OpenAIAuth.Organizations) > 0 {
		item.Organization = claims.OpenAIAuth.Organizations[0].ID
	}
	if item.UserID == "" {
		item.UserID = strings.TrimSpace(claims.Sub)
	}
	return nil
}

// decodeCodexJWTClaims decodes the middle segment of an exactly three-part
// token into metadata. Header, signature, and issuer are unchecked; malformed
// payload returns an error for the normalization caller to interpret.
func decodeCodexJWTClaims(token string) (*CodexJWTClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT format")
	}
	payload, err := decodeCodexJWTSegment(parts[1])
	if err != nil {
		return nil, err
	}
	var claims CodexJWTClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}
	return &claims, nil
}

// decodeCodexJWTSegment tries raw URL, raw standard, padded URL, and padded
// standard base64 in that order. Earlier decode failures permit compatibility
// fallback; the final standard decoder supplies any returned error.
func decodeCodexJWTSegment(segment string) ([]byte, error) {
	if decoded, err := base64.RawURLEncoding.DecodeString(segment); err == nil {
		return decoded, nil
	}
	// A URL-alphabet failure permits historical standard-base64 payloads before
	// padding is added. Header and signature remain outside this compatibility
	// decoding decision.
	if decoded, err := base64.RawStdEncoding.DecodeString(segment); err == nil {
		return decoded, nil
	}
	// Add only the missing four-character block padding after raw decoders fail.
	// The padded URL decoder precedes standard decoding for the same payload
	// bytes.
	padded := segment
	if rem := len(padded) % 4; rem > 0 {
		padded += strings.Repeat("=", 4-rem)
	}
	if decoded, err := base64.URLEncoding.DecodeString(padded); err == nil {
		return decoded, nil
	}
	return base64.StdEncoding.DecodeString(padded)
}

// buildCodexImportProviderName selects trimmed name, email, account, then user
// hint for a nonnil item. If every candidate is blank, it formats the supplied
// source index into the import fallback label.
func buildCodexImportProviderName(item *CodexImportProvider, index int) string {
	for _, candidate := range []string{item.Name, item.Email, item.ProviderID, item.UserID} {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			return candidate
		}
	}
	return fmt.Sprintf("Codex 导入提供商 %d", index)
}

// buildCodexCreateProviderName selects a trimmed administrator base or item
// name. Multiple entries suffix that base with their supplied index; nil item
// with empty base uses the generated fallback label.
func buildCodexCreateProviderName(base string, item *CodexImportProvider, index, total int) string {
	base = strings.TrimSpace(base)
	if base == "" {
		if item == nil {
			return fmt.Sprintf("Codex 导入提供商 %d", index)
		}
		return item.Name
	}
	if total > 1 {
		return fmt.Sprintf("%s #%d", base, index)
	}
	return base
}

// ResolveCodexImportExpiry returns provider expiry, credential expiry, pause
// choice, and warnings for normalized token mode. Nil item fails, and Agent
// Identity returns empty choices before reading the clock.
//
// Access-only input requires token or positive request expiry and uses the
// earlier available value for both lifetimes. Its inclusive grace cutoff
// rejects equality and forces pause even when the request disables it.
// Renewable input takes provider expiry only from a positive request value,
// retains token expiry separately, and returns the supplied pause pointer.
// Callers pass a nonnil clock when access-only validation reaches it and keep
// the returned pause pointer stable when it is borrowed from the request.
func ResolveCodexImportExpiry(req CodexSessionImportRequest, item *CodexImportProvider, now func() time.Time) (*int64, *time.Time, *bool, []string, error) {
	if item == nil {
		return nil, nil, nil, nil, errors.New("import entry is empty")
	}
	// Agent Identity runtime/task recovery is managed upstream and has no OAuth
	// access-token expiry.
	// Do not apply OAuth expiry rejection or automatic pause rules to these
	// imports.
	if item.IsAgentIdentity {
		return nil, nil, nil, nil, nil
	}

	var requestExpiresAt *time.Time
	if req.ExpiresAt != nil && *req.ExpiresAt > 0 {
		t := time.Unix(*req.ExpiresAt, 0).UTC()
		requestExpiresAt = &t
	}

	var providerExpiresAt *time.Time
	var credentialExpiresAt *time.Time
	warnings := make([]string, 0, 2)
	if item.RefreshToken == "" {
		if item.TokenExpiresAt != nil {
			tokenExpiresAt := item.TokenExpiresAt.UTC()
			providerExpiresAt = &tokenExpiresAt
			credentialExpiresAt = &tokenExpiresAt
		}
		if requestExpiresAt != nil {
			providerExpiresAt = earlierCodexTime(providerExpiresAt, requestExpiresAt)
			credentialExpiresAt = earlierCodexTime(credentialExpiresAt, requestExpiresAt)
		}
		if providerExpiresAt == nil {
			return nil, nil, nil, nil, errors.New("refresh_token is missing and accessToken expiry could not be parsed; set an expiry time in the first step before importing")
		}
		if providerExpiresAt.Unix() <= now().UTC().Unix()-codexImportClockSkewSeconds {
			return nil, nil, nil, nil, fmt.Errorf("expiry time is in the past: %s", providerExpiresAt.Format(time.RFC3339))
		}
		warnings = append(warnings, "refresh_token is missing; scheduling will stop automatically at accessToken/provider expiry")
		if req.AutoPauseOnExpired != nil && !*req.AutoPauseOnExpired {
			warnings = append(warnings, "refresh_token is missing; automatic suspension on expiry has been enabled")
		}
		autoPause := true
		expiresAtUnix := providerExpiresAt.Unix()
		return &expiresAtUnix, credentialExpiresAt, &autoPause, warnings, nil
	}

	if requestExpiresAt != nil {
		providerExpiresAt = requestExpiresAt
	}
	if item.TokenExpiresAt != nil {
		tokenExpiresAt := item.TokenExpiresAt.UTC()
		credentialExpiresAt = &tokenExpiresAt
	}
	var expiresAtUnix *int64
	if providerExpiresAt != nil {
		v := providerExpiresAt.Unix()
		expiresAtUnix = &v
	}
	return expiresAtUnix, credentialExpiresAt, req.AutoPauseOnExpired, warnings, nil
}

// earlierCodexTime selects the earlier available time and copies it into UTC
// storage. Nil candidate returns the original current pointer, so that branch
// retains borrowing; other nonnil selections allocate a separate time.
func earlierCodexTime(current, candidate *time.Time) *time.Time {
	if candidate == nil {
		return current
	}
	if current == nil || candidate.Before(*current) {
		t := candidate.UTC()
		return &t
	}
	t := current.UTC()
	return &t
}

// SanitizeCodexImportCredentialExtras trims keys and excludes its
// case-insensitive reserved token, identity, expiry, client, and signing keys.
// Empty input or no retained keys returns nil.
//
// Accepted key spelling retains its trimmed case, and colliding trimmed keys
// follow map iteration order. The result copies supported nested values
// through CloneValues; opaque values outside that helper remain borrowed. This
// filtering protects the normalized credential fields named in its explicit
// reserved table while allowing unrelated administrator metadata. Callers keep
// the source map stable during filtering and copying.
func SanitizeCodexImportCredentialExtras(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	protected := map[string]struct{}{
		"access_token":               {},
		"refresh_token":              {},
		"id_token":                   {},
		"expires_at":                 {},
		"email":                      {},
		"chatgpt_account_id":         {},
		"chatgpt_user_id":            {},
		"organization_id":            {},
		"plan_type":                  {},
		"client_id":                  {},
		"auth_mode":                  {},
		"openai_auth_mode":           {},
		"token_type":                 {},
		"chatgpt_account_is_fedramp": {},
		"agent_runtime_id":           {},
		"agent_private_key":          {},
		"task_id":                    {},
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		normalizedKey := strings.TrimSpace(key)
		if normalizedKey == "" {
			continue
		}
		if _, ok := protected[strings.ToLower(normalizedKey)]; ok {
			continue
		}
		out[normalizedKey] = value
	}
	if len(out) == 0 {
		return nil
	}
	return CloneValues(out)
}

// BuildCodexImportIdentityKeys selects only a fingerprint when renewal is
// absent and trimmed access token is nonblank. Other inputs delegate to stored
// identity order, including blank-token input that can still supply
// user/account metadata keys.
func BuildCodexImportIdentityKeys(providerID, userID, email, accessToken, refreshToken string) []string {
	accessToken = strings.TrimSpace(accessToken)
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" && accessToken != "" {
		return []string{"access:" + CodexTokenFingerprint(accessToken)}
	}
	return BuildCodexStoredIdentityKeys(providerID, userID, email, accessToken)
}

// BuildCodexAgentIdentityKeys returns an account-only provider key for
// nonblank trimmed input. Blank account returns nil, and index matching still
// checks known user conflicts under that provider-prefixed key.
//
// Runtime identifiers can change when an account registers another signer, so
// those values are not import identity keys. A user can belong to multiple
// accounts; omitting user/email lookup keys keeps their signing imports
// separated by account. This policy differs from full OAuth user-first
// matching, while the shared account-key conflict check continues to reject
// different known users.
func BuildCodexAgentIdentityKeys(providerID string) []string {
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return nil
	}
	return []string{"provider:" + providerID}
}

// MergeCodexImportMap creates a nonnil merged map with incoming keys
// overriding existing keys. CloneValues copies supported nested values
// afterward; opaque objects remain borrowed, and callers keep both input maps
// stable during merge.
func MergeCodexImportMap(existing, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(existing)+len(incoming))
	maps.Copy(out, existing)
	maps.Copy(out, incoming)
	return CloneValues(out)
}

// MergeCodexImportCredentials merges maps and applies normalized
// token-presence rules using a stable item. Nil item returns the
// supported-value copy directly.
//
// Absent incoming renewal preserves existing nonblank renewal and its client
// field, or deletes both when existing renewal is unusable. Missing ID token
// removes stale identity-token data. The restored client_id value is assigned
// directly from existing input after cloning, so a mutable non-scalar client
// value remains borrowed even when CloneValues supports that type. Other
// supported merged values remain copied. Callers synchronize source maps and
// any retained opaque/client objects.
func MergeCodexImportCredentials(existing, incoming map[string]any, item *CodexImportProvider) map[string]any {
	out := MergeCodexImportMap(existing, incoming)
	if item == nil {
		return out
	}
	// Token-presence rules follow the merged copy. Retaining existing renewal
	// restores its original client value directly, so a non-scalar client can
	// reintroduce shared storage even after supported-value cloning.
	if strings.TrimSpace(item.RefreshToken) == "" {
		if CodexCredentialString(existing, "refresh_token") == "" {
			delete(out, "refresh_token")
			delete(out, "client_id")
		} else {
			out["refresh_token"] = existing["refresh_token"]
			if clientID, ok := existing["client_id"]; ok {
				out["client_id"] = clientID
			}
		}
	}
	if strings.TrimSpace(item.IDToken) == "" {
		delete(out, "id_token")
	}
	return out
}

// CodexCredentialString reads trimmed supported scalar credential text or
// returns empty. Nil maps and unsupported value types yield empty, while
// numeric representations use codexStringValue conversion for import identity
// and credential checks.
func CodexCredentialString(credentials map[string]any, key string) string {
	if credentials == nil {
		return ""
	}
	return codexStringValue(credentials[key])
}

// CodexTokenFingerprint returns hexadecimal SHA-256 of the trimmed supplied
// token.
func CodexTokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// looksLikeJSON reports whether nonempty text starts with object or array
// syntax. It performs no whitespace trimming or validation, so callers trim
// input before choosing the stream parser.
func looksLikeJSON(content string) bool {
	if content == "" {
		return false
	}
	switch content[0] {
	case '{', '[':
		return true
	default:
		return false
	}
}

// firstCodexString returns the first nonempty supported scalar from candidate
// paths. Missing, unsupported, and blank values permit the next path; returned
// strings are scalar while the source objects remain caller-owned.
func firstCodexString(obj map[string]any, paths ...[]string) string {
	for _, path := range paths {
		if value, ok := codexPathValue(obj, path); ok {
			if str := codexStringValue(value); str != "" {
				return str
			}
		}
	}
	return ""
}

// firstCodexMap returns the first exact map[string]any path and presence.
// Missing or other types permit fallback; returned maps remain borrowed,
// including empty and typed-nil maps that still report true presence.
func firstCodexMap(obj map[string]any, paths ...[]string) (map[string]any, bool) {
	for _, path := range paths {
		value, ok := codexPathValue(obj, path)
		if !ok || value == nil {
			continue
		}
		if mapped, ok := value.(map[string]any); ok {
			return mapped, true
		}
	}
	return nil, false
}

// firstCodexBool returns the first bool or successfully parsed trimmed boolean
// string. False is a usable value, failed candidates permit fallback, and
// unresolved input defaults to false without modifying its source.
func firstCodexBool(obj map[string]any, paths ...[]string) bool {
	for _, path := range paths {
		value, ok := codexPathValue(obj, path)
		if !ok {
			continue
		}
		switch value := value.(type) {
		case bool:
			return value
		case string:
			parsed, err := strconv.ParseBool(strings.TrimSpace(value))
			if err == nil {
				return parsed
			}
		}
	}
	return false
}

// copyCodexExtraString stores nonempty supported text from one source path
// into Extra. A nil destination panics when text exists, so normalization
// initializes its owned map before copying metadata hints.
func copyCodexExtraString(obj map[string]any, extra map[string]any, key string, path []string) {
	value := firstCodexString(obj, path)
	if value != "" {
		extra[key] = value
	}
}

// firstCodexTime returns the first successfully parsed timestamp among
// candidate paths. Missing or invalid representations permit fallback; a
// parsed zero or negative epoch remains a present value for later expiry
// validation.
func firstCodexTime(obj map[string]any, paths ...[]string) (time.Time, bool) {
	for _, path := range paths {
		if value, ok := codexTimeAt(obj, path); ok {
			return value, true
		}
	}
	return time.Time{}, false
}

// codexTimeAt looks up one raw path and delegates timestamp parsing. Missing
// path reports false, while accepted scalar forms produce a UTC value without
// checking whether that time is expired.
func codexTimeAt(obj map[string]any, path []string) (time.Time, bool) {
	value, ok := codexPathValue(obj, path)
	if !ok {
		return time.Time{}, false
	}
	return parseCodexTimeValue(value)
}

// codexPathValue traverses exact map[string]any objects using the supplied key
// path. Missing or differently typed intermediate values return false; the
// final raw value remains borrowed and can itself be nil.
func codexPathValue(obj map[string]any, path []string) (any, bool) {
	var current any = obj
	for _, key := range path {
		currentObj, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok := currentObj[key]
		if !ok {
			return nil, false
		}
		current = value
	}
	return current, true
}

// codexStringValue trims supported string and numeric scalar representations
// for import. Finite float values use non-exponent decimal formatting, while
// NaN and infinities retain their special spellings. Unsupported types return
// empty, and conversion does not establish valid account or token identity.
func codexStringValue(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return strings.TrimSpace(v.String())
	case float64:
		return strings.TrimSpace(strconv.FormatFloat(v, 'f', -1, 64))
	case float32:
		return strings.TrimSpace(strconv.FormatFloat(float64(v), 'f', -1, 32))
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	default:
		return ""
	}
}

// setCodexCredentialIfNotEmpty stores trimmed nonempty text in the supplied
// credentials map. Empty text skips assignment, while a nil map with usable
// text panics; normalization initializes that map first.
func setCodexCredentialIfNotEmpty(credentials map[string]any, key, value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		credentials[key] = value
	}
}

// parseCodexTimeValue accepts trimmed RFC3339Nano or integer text,
// json.Number, float64, int, and int64 timestamps. Unsupported or invalid text
// reports false; numeric inputs are interpreted through codexUnixTime.
//
// Floating numeric input truncates to int64 before unit selection, so callers
// keep it finite and within integer range. json.Number first tries an integer
// and then that float fallback. Returned times are UTC and may be zero,
// negative, or expired; expiry validation remains with the import caller
// rather than this representation parser.
func parseCodexTimeValue(value any) (time.Time, bool) {
	switch v := value.(type) {
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return time.Time{}, false
		}
		if parsed, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return parsed.UTC(), true
		}
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return codexUnixTime(n), true
		}
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return codexUnixTime(n), true
		}
		// An integer decode failure can still supply a floating timestamp.
		// Truncate that representation before the shared unit heuristic, leaving
		// finite-range and expiry requirements with the import caller.
		if f, err := v.Float64(); err == nil {
			return codexUnixTime(int64(f)), true
		}
	case float64:
		return codexUnixTime(int64(v)), true
	case int:
		return codexUnixTime(int64(v)), true
	case int64:
		return codexUnixTime(v), true
	}
	return time.Time{}, false
}

// codexUnixTime interprets values strictly above one trillion as milliseconds
// and all others as seconds. Equal-threshold and negative values therefore
// remain seconds; the returned UTC time is not checked for plausibility or
// expiry.
func codexUnixTime(value int64) time.Time {
	// Magnitude selects millisecond compatibility; negative epochs retain second
	// units.
	if value > 1_000_000_000_000 {
		return time.UnixMilli(value).UTC()
	}
	return time.Unix(value, 0).UTC()
}

// validateCodexPrivateKey requires an injected structural key validator before
// signing import. Nil returns a configuration error, and a supplied
// validator’s error propagates to normalization, which chooses its own
// user-facing diagnostic.
func validateCodexPrivateKey(validate func(string) error, key string) error {
	if validate == nil {
		return errors.New("agent identity private key validator is not configured")
	}
	return validate(key)
}
