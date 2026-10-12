package billing

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/notification/contract"
)

const (
	defaultSiteName = "TokenRouter"

	SettingKeyProviderQuotaNotifyEmails   = "provider_quota_notify_emails"
	SettingKeyProviderQuotaNotifyEnabled  = "provider_quota_notify_enabled"
	SettingKeyBalanceLowNotifyEnabled     = "balance_low_notify_enabled"
	SettingKeyBalanceLowNotifyRechargeURL = "balance_low_notify_recharge_url"
	SettingKeyBalanceLowNotifyThreshold   = "balance_low_notify_threshold"
	SettingKeySiteName                    = "site_name"
)

type NotifySettings interface {
	GetMultiple(context.Context, []string) (map[string]string, error)
	GetValue(context.Context, string) (string, error)
}
type AlertSender interface {
	SendBalanceLowEmails([]string, int64, string, string, float64, float64, string, string)
	SendQuotaAlertEmails([]string, int64, string, string, contract.QuotaDimension, float64, string)
}
type QuotaNotifyProvider struct {
	ID             int64
	Name, Platform string
	Dimensions     []QuotaNotifyDimension
}
type QuotaNotifyReader interface {
	GetByID(context.Context, int64) (*QuotaNotifyProvider, error)
}
type BalanceNotifyService struct {
	emailService AlertSender
	settingRepo  NotifySettings
	providerRepo QuotaNotifyReader
	background   func(string, func())
}

func NewBalanceNotifyService(sender AlertSender, settings NotifySettings, providers QuotaNotifyReader, background func(string, func())) *BalanceNotifyService {
	return &BalanceNotifyService{emailService: sender, settingRepo: settings, providerRepo: providers, background: background}
}

// CheckBalanceAfterDeduction checks if balance crossed below threshold after deduction.
// Notification is sent only on first crossing: oldBalance >= threshold && newBalance < threshold.
func (s *BalanceNotifyService) CheckBalanceAfterDeduction(ctx context.Context, user *UserSummary, oldBalance, cost float64) {
	if !s.CanNotifyBalance(user) {
		return
	}
	effectiveThreshold, rechargeURL, ok := s.ResolveUserEffectiveThreshold(ctx, user)
	if !ok {
		return
	}
	newBalance := oldBalance - cost
	if !CrossedDownward(oldBalance, newBalance, effectiveThreshold) {
		return
	}
	s.DispatchBalanceLowEmail(ctx, user, newBalance, effectiveThreshold, rechargeURL)
}

// CanNotifyBalance reports whether the required services exist and the user enables balance alerts.
func (s *BalanceNotifyService) CanNotifyBalance(user *UserSummary) bool {
	if user == nil || s.emailService == nil || s.settingRepo == nil {
		return false
	}
	return user.BalanceNotifyEnabled
}

// ResolveUserEffectiveThreshold combines global and user balance alert settings.
func (s *BalanceNotifyService) ResolveUserEffectiveThreshold(ctx context.Context, user *UserSummary) (effectiveThreshold float64, rechargeURL string, ok bool) {
	globalEnabled, globalThreshold, rechargeURL := s.GetBalanceNotifyConfig(ctx)
	effective, ok := EffectiveBalanceThreshold(globalEnabled, globalThreshold, user.BalanceNotifyThreshold, user.BalanceNotifyThresholdType, user.TotalRecharged)
	if !ok {
		return 0, "", false
	}
	return effective, rechargeURL, true
}

// DispatchBalanceLowEmail collects recipients and sends the alert through the injected background runner.
func (s *BalanceNotifyService) DispatchBalanceLowEmail(ctx context.Context, user *UserSummary, newBalance, threshold float64, rechargeURL string) {
	siteName := s.GetSiteName(ctx)
	recipients := s.CollectBalanceNotifyRecipients(user)
	slog.Info("CheckBalanceAfterDeduction: sending notification",
		"user_id", user.ID, "recipients", recipients, "new_balance", newBalance, "threshold", threshold)
	s.background("service/balance_notify_service.go:dispatchBalanceLowEmail", func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic in balance notification", "recover", r)
			}
		}()
		s.emailService.SendBalanceLowEmails(recipients, user.ID, user.Username, user.Email, newBalance, threshold, siteName, rechargeURL)
	})
}

// CheckProviderQuotaAfterIncrement checks if any quota dimension crossed above its notify threshold.
// When quotaState is non-nil (from DB transaction RETURNING), it is used directly for threshold
// checking, avoiding a separate DB read. Otherwise it falls back to fetching fresh provider data.
func (s *BalanceNotifyService) CheckProviderQuotaAfterIncrement(ctx context.Context, provider *QuotaNotifyProvider, cost float64, quotaState *ProviderQuotaState) {
	if provider == nil || s.emailService == nil || s.settingRepo == nil || cost <= 0 {
		return
	}
	if !s.IsProviderQuotaNotifyEnabled(ctx) {
		return
	}
	adminEmails := s.GetProviderQuotaNotifyEmails(ctx)
	if len(adminEmails) == 0 {
		return
	}

	siteName := s.GetSiteName(ctx)
	var dims []QuotaNotifyDimension
	if quotaState != nil {
		dims = quotaDimsFromCommitted(provider, quotaState)
	} else {
		freshProvider := s.FetchFreshProvider(ctx, provider)
		dims = append([]QuotaNotifyDimension(nil), freshProvider.Dimensions...)
		provider = freshProvider // use fresh data for alert metadata
	}
	s.CheckQuotaDimCrossings(provider, dims, cost, adminEmails, siteName)
}

// FetchFreshProvider reads current provider data, returning the supplied snapshot if the reader is absent or fails.
func (s *BalanceNotifyService) FetchFreshProvider(ctx context.Context, snapshot *QuotaNotifyProvider) *QuotaNotifyProvider {
	if s.providerRepo == nil {
		return snapshot
	}
	fresh, err := s.providerRepo.GetByID(ctx, snapshot.ID)
	if err != nil {
		slog.Warn("failed to fetch fresh provider for quota notify, using snapshot",
			"provider_id", snapshot.ID, "error", err)
		return snapshot
	}
	return fresh
}

// CheckQuotaDimCrossings sends an alert for each quota dimension that crosses its threshold.
func (s *BalanceNotifyService) CheckQuotaDimCrossings(provider *QuotaNotifyProvider, dims []QuotaNotifyDimension, cost float64, adminEmails []string, siteName string) {
	for _, dim := range dims {
		if threshold, ok := dim.Crossing(cost); ok {
			s.AsyncSendQuotaAlert(adminEmails, provider.ID, provider.Name, provider.Platform, dim, dim.CurrentUsed, threshold, siteName)
		}
	}
}

// AsyncSendQuotaAlert sends through the injected background runner and recovers sender panics.
func (s *BalanceNotifyService) AsyncSendQuotaAlert(adminEmails []string, providerID int64, providerName, platform string, dim QuotaNotifyDimension, newUsed, effectiveThreshold float64, siteName string) {
	s.background("service/balance_notify_service.go:asyncSendQuotaAlert", func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic in quota notification", "recover", r)
			}
		}()
		s.emailService.SendQuotaAlertEmails(adminEmails, providerID, providerName, platform, contract.QuotaDimension(dim), newUsed, siteName)
	})
}

// GetBalanceNotifyConfig reads global balance alert settings; read failures disable alerts.
func (s *BalanceNotifyService) GetBalanceNotifyConfig(ctx context.Context) (enabled bool, threshold float64, rechargeURL string) {
	keys := []string{SettingKeyBalanceLowNotifyEnabled, SettingKeyBalanceLowNotifyThreshold, SettingKeyBalanceLowNotifyRechargeURL}
	settings, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		return false, 0, ""
	}
	enabled = settings[SettingKeyBalanceLowNotifyEnabled] == "true"
	if v := settings[SettingKeyBalanceLowNotifyThreshold]; v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			threshold = f
		}
	}
	rechargeURL = settings[SettingKeyBalanceLowNotifyRechargeURL]
	return
}

// IsProviderQuotaNotifyEnabled reports whether global provider quota alerts are enabled.
func (s *BalanceNotifyService) IsProviderQuotaNotifyEnabled(ctx context.Context) bool {
	val, err := s.settingRepo.GetValue(ctx, SettingKeyProviderQuotaNotifyEnabled)
	if err != nil {
		return false
	}
	return val == "true"
}

// GetProviderQuotaNotifyEmails returns verified, enabled administrator alert addresses.
func (s *BalanceNotifyService) GetProviderQuotaNotifyEmails(ctx context.Context) []string {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyProviderQuotaNotifyEmails)
	if err != nil || strings.TrimSpace(raw) == "" || raw == "[]" {
		return nil
	}

	entries := ParseNotifyEmails(raw)
	if len(entries) == 0 {
		return nil
	}

	return FilterVerifiedEmails(entries)
}

// GetSiteName returns the configured site name, or the default if the setting is missing or unreadable.
func (s *BalanceNotifyService) GetSiteName(ctx context.Context) string {
	name, err := s.settingRepo.GetValue(ctx, SettingKeySiteName)
	if err != nil || name == "" {
		return defaultSiteName
	}
	return name
}

// FilterVerifiedEmails trims verified, enabled addresses and deduplicates them case-insensitively.
func FilterVerifiedEmails(entries []NotifyEmailSummary) []string {
	var recipients []string
	seen := make(map[string]bool)
	for _, entry := range entries {
		if entry.Disabled || !entry.Verified {
			continue
		}
		email := strings.TrimSpace(entry.Email)
		if email == "" {
			continue
		}
		lower := strings.ToLower(email)
		if seen[lower] {
			continue
		}
		seen[lower] = true
		recipients = append(recipients, email)
	}
	return recipients
}

// CollectBalanceNotifyRecipients returns verified, enabled extra alert addresses for the user.
func (s *BalanceNotifyService) CollectBalanceNotifyRecipients(user *UserSummary) []string {
	return FilterVerifiedEmails(user.BalanceNotifyExtraEmails)
}

func quotaDimsFromCommitted(provider *QuotaNotifyProvider, state *ProviderQuotaState) []QuotaNotifyDimension {
	dims := append([]QuotaNotifyDimension(nil), provider.Dimensions...)
	for i := range dims {
		switch dims[i].Name {
		case "daily":
			dims[i].CurrentUsed, dims[i].Limit = state.DailyUsed, state.DailyLimit
		case "weekly":
			dims[i].CurrentUsed, dims[i].Limit = state.WeeklyUsed, state.WeeklyLimit
		case "total":
			dims[i].CurrentUsed, dims[i].Limit = state.TotalUsed, state.TotalLimit
		}
	}
	return dims
}
