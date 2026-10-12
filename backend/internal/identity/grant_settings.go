package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/billing"
)

// Signup-grant defaults and persistent setting keys.
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

// GrantSettingsStore reads and writes signup-grant settings.
type GrantSettingsStore interface {
	GetValue(context.Context, string) (string, error)
	GetMultiple(context.Context, []string) (map[string]string, error)
	SetMultiple(context.Context, map[string]string) error
}

// GrantSettingsOptions contains startup defaults and a subscription-plan validator.
type GrantSettingsOptions struct {
	DefaultBalance     float64
	DefaultConcurrency int
	ValidatePlans      func(context.Context, []DefaultSubscriptionSetting) error
}

// GrantSettings reads signup and first-binding defaults; billing applies balance changes.
type GrantSettings struct {
	settingRepo GrantSettingsStore
	options     GrantSettingsOptions
}

type authSourceDefaultKeySet struct {
	// source identifies the authentication provider.
	source           string
	balance          string
	concurrency      string
	subscriptions    string
	grantOnSignup    string
	grantOnFirstBind string
}

// NewGrantSettings binds the settings store and subscription-plan validator.
func NewGrantSettings(repo GrantSettingsStore, options GrantSettingsOptions) *GrantSettings {
	if options.ValidatePlans == nil {
		options.ValidatePlans = func(ctx context.Context, items []DefaultSubscriptionSetting) error {
			return ValidateDefaultSubscriptionPlans(ctx, items, nil)
		}
	}
	return &GrantSettings{settingRepo: repo, options: options}
}

// GetDefaultConcurrency returns the configured limit or the startup default on read or parse failure.
func (s *GrantSettings) GetDefaultConcurrency(ctx context.Context) int {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyDefaultConcurrency)
	if err != nil {
		return s.options.DefaultConcurrency
	}
	if v, err := strconv.Atoi(value); err == nil && v > 0 {
		return v
	}
	return s.options.DefaultConcurrency
}

// GetDefaultBalance returns the configured balance or the startup default on read or parse failure.
func (s *GrantSettings) GetDefaultBalance(ctx context.Context) float64 {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyDefaultBalance)
	if err != nil {
		return s.options.DefaultBalance
	}
	if v, err := strconv.ParseFloat(value, 64); err == nil && v >= 0 {
		return v
	}
	return s.options.DefaultBalance
}

// GetDefaultSubscriptions returns configured signup subscriptions, or nil on read failure.
func (s *GrantSettings) GetDefaultSubscriptions(ctx context.Context) []DefaultSubscriptionSetting {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyDefaultSubscriptions)
	if err != nil {
		return nil
	}
	return ParseDefaultSubscriptions(value)
}

// GetAuthSourceDefaultSettings reads and parses grant settings for all authentication providers.
func (s *GrantSettings) GetAuthSourceDefaultSettings(ctx context.Context) (*AuthSourceDefaultSettings, error) {
	keys := AuthSourceSettingKeys()

	settings, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("get auth source default settings: %w", err)
	}

	return parseAuthSourceSettings(settings), nil
}

// ResolveAuthSourceGrantSettings combines global and provider defaults according to the signup or first-binding grant flag.
func (s *GrantSettings) ResolveAuthSourceGrantSettings(ctx context.Context, signupSource string, firstBind bool) (ProviderDefaultGrantSettings, bool, error) {
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

// UpdateAuthSourceDefaultSettings validates and saves grant settings for all providers.
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

func ParseDefaultSubscriptions(raw string) []DefaultSubscriptionSetting {
	return billing.ParseDefaultSubscriptions(raw)
}

func parseProviderDefaultGrantSettings(settings map[string]string, keys authSourceDefaultKeySet) ProviderDefaultGrantSettings {
	result := ProviderDefaultGrantSettings{
		Balance:          defaultAuthSourceBalance,
		Concurrency:      defaultAuthSourceConcurrency,
		Subscriptions:    []DefaultSubscriptionSetting{},
		GrantOnSignup:    false,
		GrantOnFirstBind: false,
	}

	if v, err := strconv.ParseFloat(strings.TrimSpace(settings[keys.balance]), 64); err == nil {
		result.Balance = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(settings[keys.concurrency])); err == nil {
		result.Concurrency = v
	}
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

func writeProviderDefaultGrantUpdates(updates map[string]string, keys authSourceDefaultKeySet, settings ProviderDefaultGrantSettings) {
	updates[keys.balance] = strconv.FormatFloat(settings.Balance, 'f', 8, 64)
	updates[keys.concurrency] = strconv.Itoa(settings.Concurrency)

	subscriptions := settings.Subscriptions
	if subscriptions == nil {
		subscriptions = []DefaultSubscriptionSetting{}
	}
	raw, err := json.Marshal(subscriptions)
	if err != nil {
		raw = []byte("[]")
	}
	updates[keys.subscriptions] = string(raw)
	updates[keys.grantOnSignup] = strconv.FormatBool(settings.GrantOnSignup)
	updates[keys.grantOnFirstBind] = strconv.FormatBool(settings.GrantOnFirstBind)
}

func mergeProviderDefaultGrantSettings(globalDefaults ProviderDefaultGrantSettings, providerDefaults ProviderDefaultGrantSettings) ProviderDefaultGrantSettings {
	result := ProviderDefaultGrantSettings{
		Balance:          globalDefaults.Balance,
		Concurrency:      globalDefaults.Concurrency,
		Subscriptions:    append([]DefaultSubscriptionSetting(nil), globalDefaults.Subscriptions...),
		GrantOnSignup:    providerDefaults.GrantOnSignup,
		GrantOnFirstBind: providerDefaults.GrantOnFirstBind,
	}

	// Nonnegative balances and positive concurrency values override the global defaults,
	// including values equal to the provider parser defaults.
	if providerDefaults.Balance >= 0 {
		result.Balance = providerDefaults.Balance
	}
	if providerDefaults.Concurrency > 0 {
		result.Concurrency = providerDefaults.Concurrency
	}
	if len(providerDefaults.Subscriptions) > 0 {
		result.Subscriptions = append([]DefaultSubscriptionSetting(nil), providerDefaults.Subscriptions...)
	}

	return result
}

func ValidateDefaultSubscriptionPlans(ctx context.Context, items []DefaultSubscriptionSetting, lookup func(context.Context, int64) (*billing.SubscriptionPlan, error)) error {
	return billing.ValidateDefaultSubscriptionPlans(ctx, items, lookup)
}

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

// AuthSourceSettingKeys returns setting keys grouped by authentication provider.
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

// parseAuthSourceSettings parses grant defaults for each authentication provider.
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

// encodeAuthSourceSettings encodes balances with eight decimal places and nil subscriptions as empty arrays.
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
