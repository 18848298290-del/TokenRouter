package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// codex_invite_service.go queries Codex invitation and reset-credit status and
// performs invitation and credit-consumption requests.
// Codex invitation status queries keep invitation eligibility separate from
// existing rate-limit reset credits. Eligibility or rule-query failure
// marks invitation queries unavailable while credit usage remains queryable;
// failure of the core usage request still fails status retrieval.
//
// Credit details enrich usage counts only when credits appear available or
// known entries exist. A failed detail request retains the usage observation,
// and a supplied detail count overrides the earlier count even when zero.
// Email normalization preserves the first spelling while deduplicating
// case-insensitively and enforcing a five-address limit. Consumption obtains
// its request ID from the injected callback and returns it with the requested
// credit choice; no post-consumption cache or health recovery is implemented
// by this service.

// Codex invitation constants select referral identity, per-operation email
// count, and nonfatal administrator availability diagnostics used when
// eligibility requests cannot be completed.
const (
	codexInviteResetReferralKey        = "codex_referral_persistent_invite"
	codexInviteResetMaxEmails          = 5
	codexInviteResetUnavailable        = "CODEX_INVITE_RESET_REFERRAL_UNAVAILABLE"
	codexInviteResetUnavailableMessage = "Codex referral invitations are temporarily unavailable; existing resets can still be used"
)

// codexInviteResetEmailPattern recognizes the basic address shape accepted for
// invitation submission after input splitting, trimming, and case-insensitive
// duplicate detection.
var codexInviteResetEmailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

// CodexInviteClient supplies JSON requests for invitation eligibility and
// reset-credit operations.
type CodexInviteClient interface {
	// GetJSON reads the selected protocol path and query into a raw object.
	// The service borrows its returned maps and chooses whether request failure
	// aborts the operation or degrades invitation availability.
	GetJSON(context.Context, string, map[string]string) (map[string]any, error)
	// PostJSON submits the selected account action and returns its raw object.
	// The service normalizes selected fields while retaining those borrowed maps
	// for invitation and credit-consumption diagnostics.
	PostJSON(context.Context, string, map[string]any) (map[string]any, error)
}

// CodexInviteResetOptions retains record lookup, platform client creation,
// request identity, and warnings.
type CodexInviteResetOptions struct {
	// Read is required during account preparation and supplies a stable record.
	// An absent row fails before client construction, while lookup errors
	// propagate.
	Read func(context.Context, int64) (*Record, error)
	// Client is required after supported account lookup and returns a usable
	// protocol client. Its selected record remains stable during the account
	// operation.
	Client func(context.Context, *Record) (CodexInviteClient, error)
	// NewID supplies the redeem identifier after account preparation succeeds.
	// Consume uses that value in the request and result; the callback provides
	// uniqueness when needed.
	NewID func() string
	// Warn is required for reached eligibility or credit-detail failures.
	// Those warnings accompany retained usable status instead of aborting
	// GetStatus.
	Warn func(string, ...any)
}
type (
	// CodexInviteResetService binds account invitation and reset-credit requests
	// to captured options.
	CodexInviteResetService struct {
		// Options retains callback values for each account operation. Configure
		// dependencies before calls and keep shared client/read closures stable
		// throughout protocol requests.
		Options CodexInviteResetOptions
	}
	// codexInviteResetProviderContext associates a stable account record with its
	// prepared request client.
	codexInviteResetProviderContext struct {
		// provider and client describe one prepared account operation. The
		// borrowed record remains stable while its client handles eligibility,
		// invitation, or credit requests selected by this service.
		provider *Record           // borrowed prepared account record
		client   CodexInviteClient // prepared account protocol client
	}
)

// codexInviteResetInviteState retains eligibility results independently of
// existing reset credits.
type codexInviteResetInviteState struct {
	// eligibility and rules retain independent borrowed client responses.
	// Either failed query clears its own object and makes available false;
	// callers keep retained objects stable while inspecting invitation status.
	eligibility map[string]any // borrowed raw eligibility observation
	rules       map[string]any // borrowed raw invitation rules
	available   bool           // both invitation requests succeeded
	// unavailableReason and unavailableMessage carry the administrator
	// diagnostics when available is false. Successful paired queries leave both
	// strings empty, so consumers can distinguish availability from diagnostic text.
	unavailableReason  string // stable invitation availability reason
	unavailableMessage string // administrator invitation availability text
}

// codexInviteResetCreditState associates usage counts with optional
// reset-credit detail observations.
type codexInviteResetCreditState struct {
	// availableCount begins with the usage count, substituting the number of
	// available entries in credits when that count is zero. A successful detail
	// query replaces credits and rawCredits; availableCount changes only when
	// the detail count field is present. rawCredits and each credit's Raw map
	// remain borrowed, so consumers keep those maps stable
	// while inspecting the returned status.
	availableCount int                      // retained available credit count
	credits        []CodexInviteResetCredit // normalized reset credit entries
	rawCredits     map[string]any           // borrowed raw credit observation
}

// prepareProvider loads a nonnil OpenAI OAuth-like record and creates its
// client. Missing service/reader fails configuration, absent row fails lookup,
// and unsupported authentication fails before protocol construction; reached
// Client is required and returns a usable client.
func (s *CodexInviteResetService) prepareProvider(ctx context.Context, id int64) (*codexInviteResetProviderContext, error) {
	if s == nil || s.Options.Read == nil {
		return nil, apperror.InternalServer("CODEX_INVITE_RESET_SERVICE_NOT_CONFIGURED", "codex invite reset service is not configured")
	}
	value, err := s.Options.Read(ctx, id)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, apperror.NotFound("PROVIDER_NOT_FOUND", "provider not found")
	}
	if !value.IsOpenAIOAuth() {
		return nil, apperror.BadRequest("CODEX_INVITE_RESET_UNSUPPORTED_PROVIDER", "only OpenAI OAuth providers support Codex invite reset")
	}
	client, err := s.Options.Client(ctx, value)
	if err != nil {
		return nil, err
	}
	return &codexInviteResetProviderContext{provider: value, client: client}, nil
}

// getJSON delegates prepared account lookup to its platform JSON client.
func (s *CodexInviteResetService) getJSON(ctx context.Context, value *codexInviteResetProviderContext, path string, query map[string]string) (map[string]any, error) {
	return value.client.GetJSON(ctx, path, query)
}

// postJSON delegates the supplied account action to its prepared JSON client.
func (s *CodexInviteResetService) postJSON(ctx context.Context, value *codexInviteResetProviderContext, path string, body map[string]any) (map[string]any, error) {
	return value.client.PostJSON(ctx, path, body)
}

// GetStatus returns invitation availability alongside reset-credit status for
// a loaded account. Required options bind record/client access, and reached
// warning paths need Warn.
//
// Eligibility and rules are queried independently of the subsequent core usage
// read. Either invitation failure produces nonfatal availability diagnostics,
// while usage failure aborts the result. Credit-detail failure keeps the
// usage-derived count and entries. Successful details replace the credit list
// and raw object; a present detail count overrides the initial count, otherwise
// that initial count remains. Raw objects remain borrowed from protocol results,
// so callers synchronize any subsequent mutable edits.
// Missing consent metadata defaults to requiring confirmation.
func (s *CodexInviteResetService) GetStatus(ctx context.Context, providerID int64) (*CodexInviteResetStatus, error) {
	providerCtx, err := s.prepareProvider(ctx, providerID)
	if err != nil {
		return nil, err
	}

	inviteState := s.getInviteState(ctx, providerCtx)
	creditState, err := s.getCreditState(ctx, providerCtx)
	if err != nil {
		return nil, err
	}

	hasRewards := codexInviteResetOptionalBoolFromMap(inviteState.eligibility, "has_rewards")
	grantAction := codexInviteResetStringFromMap(inviteState.eligibility, "grant_action")

	return &CodexInviteResetStatus{
		ReferralKey:              codexInviteResetReferralKey,
		InviteEligibility:        inviteState.eligibility,
		EligibilityRules:         normalizeCodexInviteResetRules(inviteState.rules),
		ShouldShow:               codexInviteResetOptionalBoolFromMap(inviteState.eligibility, "should_show"),
		GrantAction:              grantAction,
		GrantAmount:              codexInviteResetOptionalIntFromMap(inviteState.eligibility, "grant_amount"),
		HasRewards:               hasRewards,
		GrantType:                normalizeCodexInviteResetGrantType(hasRewards, grantAction),
		InviteAvailable:          inviteState.available,
		InviteUnavailableReason:  inviteState.unavailableReason,
		InviteUnavailableMessage: inviteState.unavailableMessage,
		RequiresConsent:          codexInviteResetBoolFromMapDefault(inviteState.eligibility, "requires_explicit_confirmation", true),
		AvailableCount:           creditState.availableCount,
		Credits:                  creditState.credits,
		RawEligibilityRules:      inviteState.rules,
		RawCredits:               creditState.rawCredits,
	}, nil
}

// getInviteState queries both eligibility and invitation rules for a prepared
// account. Either error is logged and clears its own raw result, making
// invitations unavailable while preserving the other response.
//
// The returned state records a stable reason/message for that unavailable
// entry point, allowing GetStatus to continue querying existing credits.
// Success requires both requests to return nil error, even when their raw
// objects are empty. Calls are sequential, reached Warn is required, and
// returned maps remain borrowed from the platform client.
func (s *CodexInviteResetService) getInviteState(ctx context.Context, providerCtx *codexInviteResetProviderContext) codexInviteResetInviteState {
	eligibility, eligibilityErr := s.getJSON(ctx, providerCtx, "/referrals/invite/eligibility", map[string]string{
		"referral_key":                codexInviteResetReferralKey,
		"supports_rewardless_invites": codexInviteResetSupportsRewardless,
	})
	rules, rulesErr := s.getJSON(ctx, providerCtx, "/wham/referrals/eligibility_rules", map[string]string{
		"referral_key": codexInviteResetReferralKey,
	})

	if eligibilityErr == nil && rulesErr == nil {
		return codexInviteResetInviteState{
			eligibility: eligibility,
			rules:       rules,
			available:   true,
		}
	}
	// Each failed eligibility or rules query clears its own response and emits
	// a warning. GetStatus can still obtain existing credits after either
	// invitation request fails.
	if eligibilityErr != nil {
		s.Options.Warn("codex_invite_reset_eligibility_unavailable", "provider_id", providerCtx.provider.ID, "error", eligibilityErr)
		eligibility = nil
	}
	if rulesErr != nil {
		s.Options.Warn("codex_invite_reset_rules_unavailable", "provider_id", providerCtx.provider.ID, "error", rulesErr)
		rules = nil
	}

	// Keep credit retrieval available after either invitation request fails.
	// This availability diagnostic describes the invitation entry point while
	// later usage results still describe existing reset credits.
	return codexInviteResetInviteState{
		eligibility:        eligibility,
		rules:              rules,
		available:          false,
		unavailableReason:  codexInviteResetUnavailable,
		unavailableMessage: codexInviteResetUnavailableMessage,
	}
}

// getCreditState returns usage totals enriched by optional reset-credit
// details. Core usage failure propagates, and the prepared client plus reached
// Warn remain required.
//
// A zero usage count first falls back to eligible listed entries. When count
// is nonpositive and no entries exist, no detail query runs; otherwise its
// failure retains the initial state. Successful details replace the list/raw
// object, with an explicit available_count overriding prior count even at
// zero. Without that count, the prior usage-derived total remains. Returned
// raw maps and entry objects are borrowed.
func (s *CodexInviteResetService) getCreditState(ctx context.Context, providerCtx *codexInviteResetProviderContext) (codexInviteResetCreditState, error) {
	usage, err := s.getJSON(ctx, providerCtx, "/wham/usage", map[string]string{
		"supports_rewardless_invites": codexInviteResetSupportsRewardless,
	})
	if err != nil {
		return codexInviteResetCreditState{}, err
	}

	usageCredits := codexInviteResetMapFromMap(usage, "rate_limit_reset_credits")
	state := codexInviteResetCreditState{
		availableCount: codexInviteResetIntFromMap(usageCredits, "available_count"),
		credits:        normalizeCodexInviteResetCredits(usageCredits),
		rawCredits:     usageCredits,
	}
	// Zero count can be absent or explicitly supplied. Derive availability
	// from listed status labels in either case before deciding whether details
	// need a separate request.
	if state.availableCount == 0 {
		state.availableCount = countAvailableCodexInviteResetCredits(state.credits)
	}
	if state.availableCount <= 0 && len(state.credits) == 0 {
		return state, nil
	}

	details, detailsErr := s.getJSON(ctx, providerCtx, "/wham/rate-limit-reset-credits", nil)
	// Detail failure keeps the successful core usage observation, so existing
	// credit count and entries can still be shown despite unavailable enrichment.
	if detailsErr != nil {
		s.Options.Warn("codex_invite_reset_credit_details_unavailable", "provider_id", providerCtx.provider.ID, "error", detailsErr)
		return state, nil
	}

	state.credits = normalizeCodexInviteResetCredits(details)
	state.rawCredits = details
	if availableCount := codexInviteResetOptionalIntFromMap(details, "available_count"); availableCount != nil {
		state.availableCount = *availableCount
	}
	return state, nil
}

// SendInvite validates and normalizes email input before submitting it for a
// prepared account. It preserves first address spelling, deduplicates without
// case, and returns borrowed raw response objects; input or protocol failure
// aborts the operation.
func (s *CodexInviteResetService) SendInvite(ctx context.Context, providerID int64, emails []string) (*CodexInviteResetInviteResult, error) {
	providerCtx, err := s.prepareProvider(ctx, providerID)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeCodexInviteEmails(emails)
	if err != nil {
		return nil, err
	}

	raw, err := s.postJSON(ctx, providerCtx, "/wham/referrals/invite", map[string]any{
		"referral_key": codexInviteResetReferralKey,
		"emails":       normalized,
	})
	if err != nil {
		return nil, err
	}

	return &CodexInviteResetInviteResult{
		Invites:      codexInviteResetMapSliceFromMap(raw, "invites"),
		FailedEmails: codexInviteResetStringSliceFromMap(raw, "failed_emails"),
		Message:      codexInviteResetStringFromMap(raw, "message"),
		Raw:          raw,
	}, nil
}

// Consume obtains a redeem request ID from NewID and submits a trimmed
// optional credit ID. Empty choice omits credit_id for upstream selection;
// a supplied choice is retained in the returned result.
//
// The supplied ID is returned alongside upstream response data so callers can
// correlate this submission. After successful account preparation, Consume
// calls NewID and PostJSON once each; the configured client owns any retries
// during that PostJSON call. A present available_count becomes a pointer even
// when conversion yields zero, and remaining credit/raw objects stay borrowed.
// Callers supply the reached NewID callback and coordinate repeated operations
// because this service adds no local rollback or post-reset recovery.
func (s *CodexInviteResetService) Consume(ctx context.Context, providerID int64, creditID string) (*CodexInviteResetConsumeResult, error) {
	providerCtx, err := s.prepareProvider(ctx, providerID)
	if err != nil {
		return nil, err
	}
	creditID = strings.TrimSpace(creditID)
	redeemRequestID := s.Options.NewID()

	payload := map[string]any{
		"redeem_request_id": redeemRequestID,
	}
	// Include a supplied credit ID in the request. Automatic selection leaves
	// that field absent and returns the empty input as CreditID; response credit
	// identity remains in Raw.
	if creditID != "" {
		payload["credit_id"] = creditID
	}
	raw, err := s.postJSON(ctx, providerCtx, "/wham/rate-limit-reset-credits/consume", payload)
	if err != nil {
		return nil, err
	}

	var availableCount *int
	if _, ok := raw["available_count"]; ok {
		v := codexInviteResetIntFromMap(raw, "available_count")
		availableCount = &v
	}
	return &CodexInviteResetConsumeResult{
		Code:             codexInviteResetStringFromMap(raw, "code"),
		CreditID:         creditID,
		RedeemRequestID:  redeemRequestID,
		WindowsReset:     codexInviteResetIntFromMap(raw, "windows_reset"),
		AvailableCount:   availableCount,
		RemainingCredits: codexInviteResetMapSliceFromMap(raw, "credits"),
		Raw:              raw,
	}, nil
}

// normalizeCodexInviteEmails splits administrator address text and returns
// unique validated addresses. Whitespace-only pieces are omitted; basic
// malformed shape, more than five unique addresses, or no retained addresses
// fails.
//
// Case-insensitive keys remove duplicates while preserving the first supplied
// spelling and order. The limit is checked after a new address is added, so
// repeated case variants do not consume more slots. The output slice is new,
// and validation provides only the configured address-shape check rather than
// proving a mailbox exists.
func normalizeCodexInviteEmails(emails []string) ([]string, error) {
	result := make([]string, 0, len(emails))
	seen := make(map[string]struct{}, len(emails))
	for _, raw := range emails {
		for _, part := range splitCodexInviteEmailInput(raw) {
			email := strings.TrimSpace(part)
			if email == "" {
				continue
			}
			// Lowercase keys identify duplicate addresses while the first spelling
			// stays in the invitation payload. Later case variants reuse that
			// address's slot in the five-email limit.
			key := strings.ToLower(email)
			if _, exists := seen[key]; exists {
				continue
			}
			if !codexInviteResetEmailPattern.MatchString(email) {
				return nil, apperror.BadRequest("CODEX_INVITE_RESET_INVALID_EMAIL", fmt.Sprintf("invalid email: %s", email))
			}
			seen[key] = struct{}{}
			result = append(result, email)
			// Count unique accepted addresses after case-insensitive deduplication,
			// so repeating an existing spelling cannot consume the per-request limit.
			if len(result) > codexInviteResetMaxEmails {
				return nil, apperror.BadRequest("CODEX_INVITE_RESET_EMAIL_LIMIT", fmt.Sprintf("At most %d email addresses can be invited at once", codexInviteResetMaxEmails))
			}
		}
	}
	if len(result) == 0 {
		return nil, apperror.BadRequest("CODEX_INVITE_RESET_EMAILS_REQUIRED", "emails are required")
	}
	return result, nil
}

// splitCodexInviteEmailInput splits comma, semicolon, newline, carriage
// return, tab, and space separators.
func splitCodexInviteEmailInput(input string) []string {
	return strings.FieldsFunc(input, func(r rune) bool {
		switch r {
		case ',', ';', '\n', '\r', '\t', ' ':
			return true
		default:
			return false
		}
	})
}

// normalizeCodexInviteResetCredits accepts the first nonempty compatible
// credit list and skips entries with no ID. Alternate reset/expiry spellings
// are normalized into new structs; Raw retains each borrowed item object
// without expiry validation.
func normalizeCodexInviteResetCredits(raw map[string]any) []CodexInviteResetCredit {
	items := firstNonEmptyCodexInviteResetMapSlice(raw, "credits", "rate_limit_reset_credits", "items", "data")
	credits := make([]CodexInviteResetCredit, 0, len(items))
	for _, item := range items {
		id := codexInviteResetStringFromMap(item, "id")
		if id == "" {
			continue
		}
		credits = append(credits, CodexInviteResetCredit{
			ID:              id,
			Status:          codexInviteResetStringFromMap(item, "status"),
			Title:           codexInviteResetStringFromMap(item, "title"),
			Description:     codexInviteResetStringFromMap(item, "description"),
			ResetType:       codexInviteResetFirstStringFromMap(item, "reset_type", "resetType"),
			GrantedAt:       codexInviteResetFirstStringFromMap(item, "granted_at", "grantedAt"),
			ExpiresAt:       codexInviteResetFirstStringFromMap(item, "expires_at", "expiresAt"),
			ProfileUserID:   codexInviteResetStringFromMap(item, "profile_user_id"),
			ProfileImageURL: codexInviteResetStringFromMap(item, "profile_image_url"),
			Raw:             item,
		})
	}
	return credits
}

// firstNonEmptyCodexInviteResetMapSlice selects the first container with at
// least one typed object. Empty or incompatible containers permit later-key
// fallback, and the returned slice contains borrowed map elements.
func firstNonEmptyCodexInviteResetMapSlice(raw map[string]any, keys ...string) []map[string]any {
	// Credit normalization supplies alternate response keys in preference order;
	// empty or incompatible lists let the next spelling supply the credit
	// entries.
	for _, key := range keys {
		if items := codexInviteResetMapSliceFromMap(raw, key); len(items) > 0 {
			return items
		}
	}
	return nil
}

// countAvailableCodexInviteResetCredits counts case-insensitive available
// status labels from normalized entries.
func countAvailableCodexInviteResetCredits(credits []CodexInviteResetCredit) int {
	availableCount := 0
	for _, credit := range credits {
		if strings.EqualFold(credit.Status, "available") {
			availableCount++
		}
	}
	return availableCount
}

// normalizeCodexInviteResetRules accepts strings or object text from a typed
// rules array. Object fields use text/description/message/title order, blank
// values are omitted, and unresolved or differently typed input yields no
// rules.
func normalizeCodexInviteResetRules(raw map[string]any) []string {
	rulesRaw, ok := raw["rules"].([]any)
	if !ok {
		return nil
	}
	rules := make([]string, 0, len(rulesRaw))
	for _, item := range rulesRaw {
		switch value := item.(type) {
		case string:
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				rules = append(rules, trimmed)
			}
		case map[string]any:
			// Select one nonempty converted value per rule object in preference
			// order, so multiple supported text fields in that object cannot
			// produce duplicate rule lines.
			for _, key := range []string{"text", "description", "message", "title"} {
				if text := codexInviteResetStringFromMap(value, key); text != "" {
					rules = append(rules, text)
					break
				}
			}
		}
	}
	return rules
}

// normalizeCodexInviteResetGrantType selects an administrator reward enum from
// presence and action. An explicit false HasRewards wins even when action
// names a recognized reward.
//
// True or absent reward flag both defer to trimmed case-sensitive action text.
// rate_limit_reset_credit maps to rate_limit_reset and workspace_credits keeps
// that label; other actions return unknown. This permits the raw action to
// remain separately visible while the normalized result distinguishes an
// observed no-reward flag from unavailable or future reward metadata.
func normalizeCodexInviteResetGrantType(hasRewards *bool, action string) string {
	if hasRewards != nil && !*hasRewards {
		return "none"
	}
	// Translate reward actions into administrator grant labels while GetStatus
	// exposes the action separately. Unrecognized action text remains visible
	// alongside the unknown label.
	switch strings.TrimSpace(action) {
	case "rate_limit_reset_credit":
		return "rate_limit_reset"
	case "workspace_credits":
		return "workspace_credits"
	default:
		return "unknown"
	}
}

// codexInviteResetStringFromMap trims string, fmt.Stringer, or formatted
// scalar/object text. Missing keys and nil interfaces return empty; the fallback
// formatting supplies diagnostic/display text rather than validating upstream
// field shape.
func codexInviteResetStringFromMap(raw map[string]any, key string) string {
	if raw == nil {
		return ""
	}
	value, ok := raw[key]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		// Format other field types so invitation messages and credit labels can
		// retain diagnostic text when an upstream object has an unexpected
		// shape.
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

// codexInviteResetFirstStringFromMap selects the first nonempty converted
// value in key order. Blank or missing candidates allow later keys to provide
// display text; unresolved input returns empty.
func codexInviteResetFirstStringFromMap(raw map[string]any, keys ...string) string {
	// Credit normalization passes alternate reset and timestamp spellings here;
	// a blank earlier field lets the later spelling supply the displayed value.
	for _, key := range keys {
		if value := codexInviteResetStringFromMap(raw, key); value != "" {
			return value
		}
	}
	return ""
}

// codexInviteResetMapFromMap returns the borrowed exact map[string]any field
// or nil when absent or differently typed. Keep its nested values stable while
// status normalization reads them, or copy supported values before editing.
func codexInviteResetMapFromMap(raw map[string]any, key string) map[string]any {
	if raw == nil {
		return nil
	}
	value, _ := raw[key].(map[string]any)
	return value
}

// codexInviteResetIntFromMap converts int, int64, float64, or json.Number
// fields to int; missing and unsupported types return zero. Finite floats
// whose truncated values fit int discard their fractions; other floats are
// converted without validity checks. Integer conversion can narrow to the
// platform's int width. json.Number.Int64 syntax errors yield zero; range
// errors retain a signed int64 limit before conversion to int. Callers needing
// valid counts supply finite, representable numeric values.
//
// openai.CodexInviteClient decodes JSON numbers as float64; injected clients may
// supply json.Number, whose conversion still follows these rules.
func codexInviteResetIntFromMap(raw map[string]any, key string) int {
	if raw == nil {
		return 0
	}
	switch value := raw[key].(type) {
	case int:
		return value
	case int64:
		// On thirty-two-bit platforms, count conversion discards the high bits.
		return int(value)
	case float64:
		// Convert directly after numeric type recognition; callers needing
		// validated counts check finite values and int range before supplying this
		// field.
		return int(value)
	case json.Number:
		// Discard Int64's error and pass its numeric result through int conversion.
		// Optional readers still allocate a pointer for present keys when parsing
		// reports a syntax or range error.
		i, _ := value.Int64()
		return int(i)
	default:
		return 0
	}
}

// codexInviteResetOptionalIntFromMap returns nil for absent keys and a new
// pointer for present values. Present nil or unsupported input becomes zero
// through conversion, so presence is retained independently of numeric
// validity.
func codexInviteResetOptionalIntFromMap(raw map[string]any, key string) *int {
	if raw == nil {
		return nil
	}
	if _, ok := raw[key]; !ok {
		return nil
	}
	value := codexInviteResetIntFromMap(raw, key)
	return &value
}

// codexInviteResetOptionalBoolFromMap preserves bool or string presence in a
// new pointer. Trimmed case-insensitive true text sets true; every other
// string sets false, while missing/nil or other types return nil.
func codexInviteResetOptionalBoolFromMap(raw map[string]any, key string) *bool {
	if raw == nil {
		return nil
	}
	value, ok := raw[key]
	if !ok || value == nil {
		return nil
	}
	result := false
	switch v := value.(type) {
	case bool:
		result = v
	case string:
		result = strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		return nil
	}
	return &result
}

// codexInviteResetBoolFromMapDefault reads an exact boolean or the supplied
// fallback. Missing, nil, and other types select fallback, which GetStatus
// uses to require administrator consent when its observation is unavailable.
func codexInviteResetBoolFromMapDefault(raw map[string]any, key string, fallback bool) bool {
	if raw == nil {
		return fallback
	}
	value, ok := raw[key]
	if !ok || value == nil {
		return fallback
	}
	if b, ok := value.(bool); ok {
		return b
	}
	return fallback
}

// codexInviteResetStringSliceFromMap converts a typed any array into trimmed
// formatted strings. Blank formatted text is skipped, other values use
// fmt.Sprint, and absent or differently typed arrays return nil.
func codexInviteResetStringSliceFromMap(raw map[string]any, key string) []string {
	values, ok := raw[key].([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if s := strings.TrimSpace(fmt.Sprint(value)); s != "" {
			result = append(result, s)
		}
	}
	return result
}

// codexInviteResetMapSliceFromMap selects exact map objects from a typed any
// array. Non-map members are skipped, typed-nil maps can remain elements, and
// returned map values stay borrowed from the protocol response.
func codexInviteResetMapSliceFromMap(raw map[string]any, key string) []map[string]any {
	values, ok := raw[key].([]any)
	if !ok {
		return nil
	}
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if item, ok := value.(map[string]any); ok {
			result = append(result, item)
		}
	}
	return result
}
