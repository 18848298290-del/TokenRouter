package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// grant_settings.go resolves and encodes provider-specific signup and
// first-binding grant settings.
// Signup and first-binding grants use separate provider switches while
// sharing global fallback amounts and plan selections. An enabled provider
// can contribute its parser defaults even when balance or concurrency text is
// absent, whereas an empty provider plan list retains global subscriptions.
//
// Administrators write a flat string map after validating each provider list.
// Encoding fixes finite balance precision and normalizes nil subscription lists,
// while reads distinguish storage failures, invalid values and action
// enablement. ResolveAuthSourceGrantSettings combines separately read global
// settings with the provider batch, leaving snapshot consistency and
// persistence completion to the owning settings workflow.

// Provider parser defaults and persistent grant keys select signup/first-bind
// amounts, concurrency, plan lists and action switches for the recognized authentication sources.
const (
	defaultAuthSourceBalance                            = 0
	defaultAuthSourceConcurrency                        = 5
	SettingKeyAuthSourceDefaultDingTalkBalance          = "auth_source_default_dingtalk_balance"
	SettingKeyAuthSourceDefaultDingTalkConcurrency      = "auth_source_default_dingtalk_concurrency"
	SettingKeyAuthSourceDefaultDingTalkGrantOnFirstBind = "auth_source_default_dingtalk_grant_on_first_bind"
	SettingKeyAuthSourceDefaultDingTalkGrantOnSignup    = "auth_source_default_dingtalk_grant_on_signup"
	SettingKeyAuthSourceDefaultDingTalkSubscriptions    = "auth_source_default_dingtalk_subscriptions"
	SettingKeyAuthSourceDefaultEmailBalance             = "auth_source_default_email_balance"
	SettingKeyAuthSourceDefaultEmailConcurrency         = "auth_source_default_email_concurrency"
	SettingKeyAuthSourceDefaultEmailGrantOnFirstBind    = "auth_source_default_email_grant_on_first_bind"
	SettingKeyAuthSourceDefaultEmailGrantOnSignup       = "auth_source_default_email_grant_on_signup"
	SettingKeyAuthSourceDefaultEmailSubscriptions       = "auth_source_default_email_subscriptions"
	SettingKeyAuthSourceDefaultGitHubBalance            = "auth_source_default_github_balance"
	SettingKeyAuthSourceDefaultGitHubConcurrency        = "auth_source_default_github_concurrency"
	SettingKeyAuthSourceDefaultGitHubGrantOnFirstBind   = "auth_source_default_github_grant_on_first_bind"
	SettingKeyAuthSourceDefaultGitHubGrantOnSignup      = "auth_source_default_github_grant_on_signup"
	SettingKeyAuthSourceDefaultGitHubSubscriptions      = "auth_source_default_github_subscriptions"
	SettingKeyAuthSourceDefaultGoogleBalance            = "auth_source_default_google_balance"
	SettingKeyAuthSourceDefaultGoogleConcurrency        = "auth_source_default_google_concurrency"
	SettingKeyAuthSourceDefaultGoogleGrantOnFirstBind   = "auth_source_default_google_grant_on_first_bind"
	SettingKeyAuthSourceDefaultGoogleGrantOnSignup      = "auth_source_default_google_grant_on_signup"
	SettingKeyAuthSourceDefaultGoogleSubscriptions      = "auth_source_default_google_subscriptions"
	SettingKeyAuthSourceDefaultLinuxDoBalance           = "auth_source_default_linuxdo_balance"
	SettingKeyAuthSourceDefaultLinuxDoConcurrency       = "auth_source_default_linuxdo_concurrency"
	SettingKeyAuthSourceDefaultLinuxDoGrantOnFirstBind  = "auth_source_default_linuxdo_grant_on_first_bind"
	SettingKeyAuthSourceDefaultLinuxDoGrantOnSignup     = "auth_source_default_linuxdo_grant_on_signup"
	SettingKeyAuthSourceDefaultLinuxDoSubscriptions     = "auth_source_default_linuxdo_subscriptions"
	SettingKeyAuthSourceDefaultOIDCBalance              = "auth_source_default_oidc_balance"
	SettingKeyAuthSourceDefaultOIDCConcurrency          = "auth_source_default_oidc_concurrency"
	SettingKeyAuthSourceDefaultOIDCGrantOnFirstBind     = "auth_source_default_oidc_grant_on_first_bind"
	SettingKeyAuthSourceDefaultOIDCGrantOnSignup        = "auth_source_default_oidc_grant_on_signup"
	SettingKeyAuthSourceDefaultOIDCSubscriptions        = "auth_source_default_oidc_subscriptions"
	SettingKeyAuthSourceDefaultWeChatBalance            = "auth_source_default_wechat_balance"
	SettingKeyAuthSourceDefaultWeChatConcurrency        = "auth_source_default_wechat_concurrency"
	SettingKeyAuthSourceDefaultWeChatGrantOnFirstBind   = "auth_source_default_wechat_grant_on_first_bind"
	SettingKeyAuthSourceDefaultWeChatGrantOnSignup      = "auth_source_default_wechat_grant_on_signup"
	SettingKeyAuthSourceDefaultWeChatSubscriptions      = "auth_source_default_wechat_subscriptions"
	SettingKeyDefaultBalance                            = "default_balance"
	SettingKeyDefaultSubscriptions                      = "default_subscriptions"
	SettingKeyForceEmailOnThirdPartySignup              = "force_email_on_third_party_signup"
)

// Provider key sets associate each authentication source with its balance,
// concurrency, subscription and independent signup/first-binding switch keys.
var (
	emailAuthSourceDefaultKeys = authSourceDefaultKeySet{
		source:           "email",
		balance:          SettingKeyAuthSourceDefaultEmailBalance,
		concurrency:      SettingKeyAuthSourceDefaultEmailConcurrency,
		subscriptions:    SettingKeyAuthSourceDefaultEmailSubscriptions,
		grantOnSignup:    SettingKeyAuthSourceDefaultEmailGrantOnSignup,
		grantOnFirstBind: SettingKeyAuthSourceDefaultEmailGrantOnFirstBind,
	}
	linuxDoAuthSourceDefaultKeys = authSourceDefaultKeySet{
		source:           "linuxdo",
		balance:          SettingKeyAuthSourceDefaultLinuxDoBalance,
		concurrency:      SettingKeyAuthSourceDefaultLinuxDoConcurrency,
		subscriptions:    SettingKeyAuthSourceDefaultLinuxDoSubscriptions,
		grantOnSignup:    SettingKeyAuthSourceDefaultLinuxDoGrantOnSignup,
		grantOnFirstBind: SettingKeyAuthSourceDefaultLinuxDoGrantOnFirstBind,
	}
	oidcAuthSourceDefaultKeys = authSourceDefaultKeySet{
		source:           "oidc",
		balance:          SettingKeyAuthSourceDefaultOIDCBalance,
		concurrency:      SettingKeyAuthSourceDefaultOIDCConcurrency,
		subscriptions:    SettingKeyAuthSourceDefaultOIDCSubscriptions,
		grantOnSignup:    SettingKeyAuthSourceDefaultOIDCGrantOnSignup,
		grantOnFirstBind: SettingKeyAuthSourceDefaultOIDCGrantOnFirstBind,
	}
	weChatAuthSourceDefaultKeys = authSourceDefaultKeySet{
		source:           "wechat",
		balance:          SettingKeyAuthSourceDefaultWeChatBalance,
		concurrency:      SettingKeyAuthSourceDefaultWeChatConcurrency,
		subscriptions:    SettingKeyAuthSourceDefaultWeChatSubscriptions,
		grantOnSignup:    SettingKeyAuthSourceDefaultWeChatGrantOnSignup,
		grantOnFirstBind: SettingKeyAuthSourceDefaultWeChatGrantOnFirstBind,
	}
	gitHubAuthSourceDefaultKeys = authSourceDefaultKeySet{
		source:           "github",
		balance:          SettingKeyAuthSourceDefaultGitHubBalance,
		concurrency:      SettingKeyAuthSourceDefaultGitHubConcurrency,
		subscriptions:    SettingKeyAuthSourceDefaultGitHubSubscriptions,
		grantOnSignup:    SettingKeyAuthSourceDefaultGitHubGrantOnSignup,
		grantOnFirstBind: SettingKeyAuthSourceDefaultGitHubGrantOnFirstBind,
	}
	googleAuthSourceDefaultKeys = authSourceDefaultKeySet{
		source:           "google",
		balance:          SettingKeyAuthSourceDefaultGoogleBalance,
		concurrency:      SettingKeyAuthSourceDefaultGoogleConcurrency,
		subscriptions:    SettingKeyAuthSourceDefaultGoogleSubscriptions,
		grantOnSignup:    SettingKeyAuthSourceDefaultGoogleGrantOnSignup,
		grantOnFirstBind: SettingKeyAuthSourceDefaultGoogleGrantOnFirstBind,
	}
	dingTalkAuthSourceDefaultKeys = authSourceDefaultKeySet{
		source:           "dingtalk",
		balance:          SettingKeyAuthSourceDefaultDingTalkBalance,
		concurrency:      SettingKeyAuthSourceDefaultDingTalkConcurrency,
		subscriptions:    SettingKeyAuthSourceDefaultDingTalkSubscriptions,
		grantOnSignup:    SettingKeyAuthSourceDefaultDingTalkGrantOnSignup,
		grantOnFirstBind: SettingKeyAuthSourceDefaultDingTalkGrantOnFirstBind,
	}
)

// GrantSettingsStore reads current grant values and persists prepared flat setting updates.
type GrantSettingsStore interface {
	// GetValue reads one global grant value as stored text. GetDefaultBalance
	// and GetDefaultConcurrency select startup values after a failed read, while
	// subscription reads select nil.
	GetValue(context.Context, string) (string, error)
	// GetMultiple reads requested provider keys into a flat value map. Missing
	// keys stay absent for parser defaults; a batch failure remains an error for
	// provider-grant resolution.
	GetMultiple(context.Context, []string) (map[string]string, error)
	// SetMultiple persists the supplied encoded grant values. The caller
	// completes validation first, and the store returns persistence errors while
	// owning the completion behavior of its batch write.
	SetMultiple(context.Context, map[string]string) error
}

// GrantSettingsOptions contains startup defaults and a subscription-plan validator.
type GrantSettingsOptions struct {
	DefaultBalance     float64 // global fallback balance in USD
	DefaultConcurrency int     // global fallback concurrent-request limit
	// ValidatePlans checks each provider list before encoding. NewGrantSettings
	// replaces nil with validation of repeated positive plan IDs without a plan
	// lookup.
	ValidatePlans func(context.Context, []DefaultSubscriptionSetting) error
}

// GrantSettings reads signup and first-binding defaults; billing applies balance changes.
type GrantSettings struct {
	settingRepo GrantSettingsStore // current grant setting persistence
	// options retains startup fallbacks and the selected validator. Validation
	// callbacks receive existing plan slices, which callers keep stable during
	// preparation.
	options GrantSettingsOptions
}

// authSourceDefaultKeySet identifies the flat setting names associated with one provider.
type authSourceDefaultKeySet struct {
	source           string // associated authentication provider label
	balance          string // provider balance setting key
	concurrency      string // provider concurrency setting key
	subscriptions    string // provider plan-list setting key
	grantOnSignup    string // provider signup switch key
	grantOnFirstBind string // provider first-binding switch key
}

// NewGrantSettings retains the supplied setting store and startup defaults. A
// nil ValidatePlans callback is replaced with billing validation without a
// plan lookup, which checks repeated positive plan IDs rather than their
// existence.
func NewGrantSettings(repo GrantSettingsStore, options GrantSettingsOptions) *GrantSettings {
	if options.ValidatePlans == nil {
		options.ValidatePlans = func(ctx context.Context, items []DefaultSubscriptionSetting) error {
			return ValidateDefaultSubscriptionPlans(ctx, items, nil)
		}
	}
	return &GrantSettings{settingRepo: repo, options: options}
}

// GetDefaultConcurrency returns a limit parsed from positive global integer
// text. Read errors, malformed text and nonpositive stored values select
// options.DefaultConcurrency as supplied, which this reader does not validate
// again.
func (s *GrantSettings) GetDefaultConcurrency(ctx context.Context) int {
	// Unavailable stored concurrency cannot supply a configured admission limit,
	// so this read uses the constructor fallback instead of failing signup.
	value, err := s.settingRepo.GetValue(ctx, SettingKeyDefaultConcurrency)
	if err != nil {
		return s.options.DefaultConcurrency
	}
	if v, err := strconv.Atoi(value); err == nil && v > 0 {
		return v
	}
	// Malformed or nonpositive text selects the same startup fallback, keeping
	// the global limit independent of provider-parser defaults and their action
	// flags.
	return s.options.DefaultConcurrency
}

// GetDefaultBalance returns a nonnegative parsed global balance or its
// startup fallback. Read, parse or negative-value rejection selects
// options.DefaultBalance; accepted ParseFloat values receive no additional
// finite-number check here.
func (s *GrantSettings) GetDefaultBalance(ctx context.Context) float64 {
	// A failed global-balance read supplies no configured amount, so retain the
	// startup fallback while later provider resolution still makes its own
	// enablement decision.
	value, err := s.settingRepo.GetValue(ctx, SettingKeyDefaultBalance)
	if err != nil {
		return s.options.DefaultBalance
	}
	if v, err := strconv.ParseFloat(value, 64); err == nil && v >= 0 {
		return v
	}
	// Rejected balance text falls back to the supplied startup amount; provider
	// defaults are parsed separately and may override that value only for an
	// enabled grant.
	return s.options.DefaultBalance
}

// GetDefaultSubscriptions reads global signup plan selections and delegates
// their parsing to billing. Read failure returns nil; invalid JSON also
// yields nil, while valid selections retain order and positive duplicates
// until separate validation.
func (s *GrantSettings) GetDefaultSubscriptions(ctx context.Context) []DefaultSubscriptionSetting {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyDefaultSubscriptions)
	// Read failure supplies no global plan selections, so signup continues with
	// nil grants rather than propagating this optional-setting storage failure.
	if err != nil {
		return nil
	}
	return ParseDefaultSubscriptions(value)
}

// GetAuthSourceDefaultSettings loads the flat keys for every recognized
// provider and parses their current grant view. Batch-read errors remain
// wrapped errors; missing values use provider parser defaults rather than the
// startup global defaults.
func (s *GrantSettings) GetAuthSourceDefaultSettings(ctx context.Context) (*AuthSourceDefaultSettings, error) {
	keys := AuthSourceSettingKeys()

	settings, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("get auth source default settings: %w", err)
	}

	return parseAuthSourceSettings(settings), nil
}

// ResolveAuthSourceGrantSettings selects a provider grant only when its
// signup or first-bind flag permits the requested action. It reads global
// defaults separately before the provider batch, so those observations need
// not come from one settings snapshot.
//
// Read failure returns the prepared global result with enabled=false and an
// error; an unknown source or disabled action returns it without error.
// Enabled provider values are merged using their numeric and nonempty-list
// rules. Parser defaults can therefore override global balance/concurrency,
// while empty provider subscriptions cannot clear a global list.
func (s *GrantSettings) ResolveAuthSourceGrantSettings(ctx context.Context, signupSource string, firstBind bool) (ProviderDefaultGrantSettings, bool, error) {
	// Read global amounts and plans before the provider batch; an unavailable or
	// disabled provider result therefore retains these separately observed
	// global fallbacks.
	result := ProviderDefaultGrantSettings{
		Balance:       s.GetDefaultBalance(ctx),
		Concurrency:   s.GetDefaultConcurrency(ctx),
		Subscriptions: s.GetDefaultSubscriptions(ctx),
	}

	defaults, err := s.GetAuthSourceDefaultSettings(ctx)
	if err != nil {
		return result, false, err
	}

	providerDefaults, ok := AuthAuthSourceSignupSettings(defaults, signupSource)
	if !ok {
		return result, false, nil
	}

	enabled := providerDefaults.GrantOnSignup
	if firstBind {
		enabled = providerDefaults.GrantOnFirstBind
	}
	if !enabled {
		return result, false, nil
	}

	return mergeProviderDefaultGrantSettings(result, providerDefaults), true, nil
}

// UpdateAuthSourceDefaultSettings validates every provider subscription list
// and saves the encoded flat values. Nil input returns success without a
// write; persistence errors remain wrapped errors, and the supplied store
// owns batch completion.
func (s *GrantSettings) UpdateAuthSourceDefaultSettings(ctx context.Context, settings *AuthSourceDefaultSettings) error {
	updates, err := s.PrepareAuthSourceDefaults(ctx, settings)
	if err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}

	if err := s.settingRepo.SetMultiple(ctx, updates); err != nil {
		return fmt.Errorf("update auth source default settings: %w", err)
	}
	return nil
}

// ParseDefaultSubscriptions delegates stored plan-selection JSON to billing.
// Empty or malformed input returns nil; positive IDs retain order and
// duplicates, while nonpositive IDs are removed before signup consumers
// receive the list.
func ParseDefaultSubscriptions(raw string) []DefaultSubscriptionSetting {
	return billing.ParseDefaultSubscriptions(raw)
}

// parseProviderDefaultGrantSettings decodes one provider's flat grant keys.
// Missing or malformed numbers retain zero balance and five concurrency;
// successful numeric parsing itself performs no sign or finite-value
// validation. Only exact true text enables each grant flag.
//
// Subscription parsing discards nonpositive plan IDs and leaves duplicate
// positive choices for separate validation. Nil parsing results retain an
// allocated empty list. These defaults are usable merge inputs when an action
// flag is enabled, so absent balance/concurrency text differs from an
// instruction to inherit all global values.
func parseProviderDefaultGrantSettings(settings map[string]string, keys authSourceDefaultKeySet) ProviderDefaultGrantSettings {
	result := ProviderDefaultGrantSettings{
		Balance:          defaultAuthSourceBalance,
		Concurrency:      defaultAuthSourceConcurrency,
		Subscriptions:    []DefaultSubscriptionSetting{},
		GrantOnSignup:    false,
		GrantOnFirstBind: false,
	}

	// Malformed saved amounts leave the initialized zero balance, which can
	// later override the global grant amount when the provider action is
	// enabled.
	if v, err := strconv.ParseFloat(strings.TrimSpace(settings[keys.balance]), 64); err == nil {
		result.Balance = v
	}
	// Malformed saved limits retain five concurrent requests, so enabling only
	// an action switch can still select a provider concurrency value above zero.
	if v, err := strconv.Atoi(strings.TrimSpace(settings[keys.concurrency])); err == nil {
		result.Concurrency = v
	}
	// Nil parsing results retain the initialized empty list, preserving the
	// provider view's array representation until merging decides whether global
	// plan selections survive.
	if items := ParseDefaultSubscriptions(settings[keys.subscriptions]); items != nil {
		result.Subscriptions = items
	}
	if raw, ok := settings[keys.grantOnSignup]; ok {
		result.GrantOnSignup = raw == "true"
	}
	if raw, ok := settings[keys.grantOnFirstBind]; ok {
		result.GrantOnFirstBind = raw == "true"
	}

	return result
}

// writeProviderDefaultGrantUpdates fills the caller's map with one provider's
// flat values. Finite balances use eight fractional digits, flags use boolean text,
// and nil subscriptions become an empty JSON array; validation belongs to the
// caller.
func writeProviderDefaultGrantUpdates(updates map[string]string, keys authSourceDefaultKeySet, settings ProviderDefaultGrantSettings) {
	// Persist finite balances at the settings format's eight-decimal precision.
	updates[keys.balance] = strconv.FormatFloat(settings.Balance, 'f', 8, 64)
	updates[keys.concurrency] = strconv.Itoa(settings.Concurrency)

	subscriptions := settings.Subscriptions
	// Normalize an absent plan list to an empty JSON array so stored provider
	// settings retain the list representation used by administrator readers.
	if subscriptions == nil {
		subscriptions = []DefaultSubscriptionSetting{}
	}
	raw, err := json.Marshal(subscriptions)
	// An encoding failure supplies no usable list text, so save the empty-array
	// representation rather than an incomplete serialization for this provider.
	if err != nil {
		raw = []byte("[]")
	}
	updates[keys.subscriptions] = string(raw)
	updates[keys.grantOnSignup] = strconv.FormatBool(settings.GrantOnSignup)
	updates[keys.grantOnFirstBind] = strconv.FormatBool(settings.GrantOnFirstBind)
}

// mergeProviderDefaultGrantSettings chooses eligible provider values and
// preserves global companions for rejected overrides. Nonnegative balance, including zero,
// overrides global funds; positive concurrency overrides the global limit,
// and a nonempty subscription list replaces global selections. Both action
// flags come from the provider.
//
// An empty provider list cannot clear global subscriptions here. Parser
// defaults of zero balance and five concurrency participate as ordinary
// overriding values when the grant is enabled. Selected plan-ID values are
// copied into a separate slice; selecting an empty global list produces nil
// rather than retaining its empty backing storage.
func mergeProviderDefaultGrantSettings(globalDefaults ProviderDefaultGrantSettings, providerDefaults ProviderDefaultGrantSettings) ProviderDefaultGrantSettings {
	result := ProviderDefaultGrantSettings{
		Balance:          globalDefaults.Balance,
		Concurrency:      globalDefaults.Concurrency,
		Subscriptions:    append([]DefaultSubscriptionSetting(nil), globalDefaults.Subscriptions...),
		GrantOnSignup:    providerDefaults.GrantOnSignup,
		GrantOnFirstBind: providerDefaults.GrantOnFirstBind,
	}

	// Balance and concurrency parser defaults remain real overrides after action
	// enablement; rejecting a provider value preserves its independently read global companion.
	if providerDefaults.Balance >= 0 {
		result.Balance = providerDefaults.Balance
	}
	if providerDefaults.Concurrency > 0 {
		result.Concurrency = providerDefaults.Concurrency
	}
	// Replace global plans only when the provider supplies selections; missing
	// or empty selections therefore retain the independently loaded global
	// signup entitlements.
	if len(providerDefaults.Subscriptions) > 0 {
		result.Subscriptions = append([]DefaultSubscriptionSetting(nil), providerDefaults.Subscriptions...)
	}

	return result
}

// ValidateDefaultSubscriptionPlans delegates signup plan validation to
// billing. Nonpositive IDs are ignored, repeated positive IDs are rejected,
// and a supplied lookup additionally checks existence while preserving other
// repository failures as errors.
func ValidateDefaultSubscriptionPlans(ctx context.Context, items []DefaultSubscriptionSetting, lookup func(context.Context, int64) (*billing.SubscriptionPlan, error)) error {
	return billing.ValidateDefaultSubscriptionPlans(ctx, items, lookup)
}

// PrepareAuthSourceDefaults validates the supplied provider lists before
// encoding a new flat settings map. Nil input returns nil without error; the
// configured callback receives the existing slices, so callers keep this
// settings view stable throughout validation and encoding.
func (s *GrantSettings) PrepareAuthSourceDefaults(ctx context.Context, settings *AuthSourceDefaultSettings) (map[string]string, error) {
	if settings == nil {
		return nil, nil
	}

	for _, subscriptions := range [][]DefaultSubscriptionSetting{
		settings.Email.Subscriptions,
		settings.LinuxDo.Subscriptions,
		settings.OIDC.Subscriptions,
		settings.WeChat.Subscriptions,
		settings.GitHub.Subscriptions,
		settings.Google.Subscriptions,
		settings.DingTalk.Subscriptions,
	} {
		if err := s.options.ValidatePlans(ctx, subscriptions); err != nil {
			return nil, err
		}
	}

	return encodeAuthSourceSettings(settings), nil
}

// AuthSourceSettingKeys returns a newly allocated list of provider-grant keys
// followed by the third-party email-policy key. Callers can modify that slice
// independently; its ordering aligns batch reads and settings-participant
// preparation.
func AuthSourceSettingKeys() []string {
	return []string{
		SettingKeyAuthSourceDefaultEmailBalance,
		SettingKeyAuthSourceDefaultEmailConcurrency,
		SettingKeyAuthSourceDefaultEmailSubscriptions,
		SettingKeyAuthSourceDefaultEmailGrantOnSignup,
		SettingKeyAuthSourceDefaultEmailGrantOnFirstBind,
		SettingKeyAuthSourceDefaultLinuxDoBalance,
		SettingKeyAuthSourceDefaultLinuxDoConcurrency,
		SettingKeyAuthSourceDefaultLinuxDoSubscriptions,
		SettingKeyAuthSourceDefaultLinuxDoGrantOnSignup,
		SettingKeyAuthSourceDefaultLinuxDoGrantOnFirstBind,
		SettingKeyAuthSourceDefaultOIDCBalance,
		SettingKeyAuthSourceDefaultOIDCConcurrency,
		SettingKeyAuthSourceDefaultOIDCSubscriptions,
		SettingKeyAuthSourceDefaultOIDCGrantOnSignup,
		SettingKeyAuthSourceDefaultOIDCGrantOnFirstBind,
		SettingKeyAuthSourceDefaultWeChatBalance,
		SettingKeyAuthSourceDefaultWeChatConcurrency,
		SettingKeyAuthSourceDefaultWeChatSubscriptions,
		SettingKeyAuthSourceDefaultWeChatGrantOnSignup,
		SettingKeyAuthSourceDefaultWeChatGrantOnFirstBind,
		SettingKeyAuthSourceDefaultGitHubBalance,
		SettingKeyAuthSourceDefaultGitHubConcurrency,
		SettingKeyAuthSourceDefaultGitHubSubscriptions,
		SettingKeyAuthSourceDefaultGitHubGrantOnSignup,
		SettingKeyAuthSourceDefaultGitHubGrantOnFirstBind,
		SettingKeyAuthSourceDefaultGoogleBalance,
		SettingKeyAuthSourceDefaultGoogleConcurrency,
		SettingKeyAuthSourceDefaultGoogleSubscriptions,
		SettingKeyAuthSourceDefaultGoogleGrantOnSignup,
		SettingKeyAuthSourceDefaultGoogleGrantOnFirstBind,
		SettingKeyAuthSourceDefaultDingTalkBalance,
		SettingKeyAuthSourceDefaultDingTalkConcurrency,
		SettingKeyAuthSourceDefaultDingTalkSubscriptions,
		SettingKeyAuthSourceDefaultDingTalkGrantOnSignup,
		SettingKeyAuthSourceDefaultDingTalkGrantOnFirstBind,
		SettingKeyForceEmailOnThirdPartySignup,
	}
}

// parseAuthSourceSettings builds all recognized provider views from one flat
// map. A nil map supplies the parser defaults, and only exact true text
// enables ForceEmailOnThirdPartySignup; plan slices come from their
// individual JSON parses.
func parseAuthSourceSettings(settings map[string]string) *AuthSourceDefaultSettings {
	return &AuthSourceDefaultSettings{
		Email:                        parseProviderDefaultGrantSettings(settings, emailAuthSourceDefaultKeys),
		LinuxDo:                      parseProviderDefaultGrantSettings(settings, linuxDoAuthSourceDefaultKeys),
		OIDC:                         parseProviderDefaultGrantSettings(settings, oidcAuthSourceDefaultKeys),
		WeChat:                       parseProviderDefaultGrantSettings(settings, weChatAuthSourceDefaultKeys),
		GitHub:                       parseProviderDefaultGrantSettings(settings, gitHubAuthSourceDefaultKeys),
		Google:                       parseProviderDefaultGrantSettings(settings, googleAuthSourceDefaultKeys),
		DingTalk:                     parseProviderDefaultGrantSettings(settings, dingTalkAuthSourceDefaultKeys),
		ForceEmailOnThirdPartySignup: settings[SettingKeyForceEmailOnThirdPartySignup] == "true",
	}
}

// encodeAuthSourceSettings allocates the flat persistence map for every
// provider and the email-policy flag. It requires nonnil input, delegates
// value encoding without validation, and writes nil subscription lists as
// empty arrays rather than null.
func encodeAuthSourceSettings(settings *AuthSourceDefaultSettings) map[string]string {
	updates := make(map[string]string, 36)
	writeProviderDefaultGrantUpdates(updates, emailAuthSourceDefaultKeys, settings.Email)
	writeProviderDefaultGrantUpdates(updates, linuxDoAuthSourceDefaultKeys, settings.LinuxDo)
	writeProviderDefaultGrantUpdates(updates, oidcAuthSourceDefaultKeys, settings.OIDC)
	writeProviderDefaultGrantUpdates(updates, weChatAuthSourceDefaultKeys, settings.WeChat)
	writeProviderDefaultGrantUpdates(updates, gitHubAuthSourceDefaultKeys, settings.GitHub)
	writeProviderDefaultGrantUpdates(updates, googleAuthSourceDefaultKeys, settings.Google)
	writeProviderDefaultGrantUpdates(updates, dingTalkAuthSourceDefaultKeys, settings.DingTalk)
	updates[SettingKeyForceEmailOnThirdPartySignup] = strconv.FormatBool(settings.ForceEmailOnThirdPartySignup)
	return updates
}
