package billing

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/notification/contract"
)

// Balance and quota notices are selected after funds or provider usage have
// changed. Balance checking compares the supplied previous funds with the
// deducted amount, while quota checking reconstructs earlier usage by
// subtracting the increment from committed totals. Each event needs its global
// switch and usable verified recipients before delivery is scheduled.
//
// A transaction quota result takes precedence over rereading usage, but
// provider dimensions still determine which thresholds apply. When that result
// is absent, a fresh provider read supplies settings and totals, retaining the
// snapshot on read failure. The injected background runner owns dispatch
// timing; callback recovery logs sender panics, and notification delivery owns
// transport and deduplication.

const (
	// Default alert branding is used when the stored site label is empty or
	// unreadable, keeping rendered messages identified to recipients.
	defaultSiteName = "TokenRouter"

	// Alert setting keys locate provider recipient lists, global switches,
	// balance thresholds, recharge links and site branding for post-charge
	// notification decisions.
	SettingKeyProviderQuotaNotifyEmails   = "provider_quota_notify_emails"
	SettingKeyProviderQuotaNotifyEnabled  = "provider_quota_notify_enabled"
	SettingKeyBalanceLowNotifyEnabled     = "balance_low_notify_enabled"
	SettingKeyBalanceLowNotifyRechargeURL = "balance_low_notify_recharge_url"
	SettingKeyBalanceLowNotifyThreshold   = "balance_low_notify_threshold"
	SettingKeySiteName                    = "site_name"
)

// NotifySettings reads stored switches, thresholds and recipient text for
// billing alerts.
type NotifySettings interface {
	// GetMultiple reads a selected settings snapshot for one balance alert
	// decision. The caller treats a returned error as disabling the global alert
	// configuration for that observation.
	GetMultiple(context.Context, []string) (map[string]string, error)
	// GetValue reads one alert setting by key using the supplied context.
	GetValue(context.Context, string) (string, error)
}

// AlertSender delivers balance and provider-quota notices to billing-selected
// recipients.
type AlertSender interface {
	// SendBalanceLowEmails delivers the supplied balance observation to selected
	// recipients. The user email supplies identity/display data; delivery owns
	// rendering and deduplication, and the caller keeps recipient storage stable
	// until the send finishes.
	SendBalanceLowEmails([]string, int64, string, string, float64, float64, string, string)
	// SendQuotaAlertEmails delivers one provider quota dimension to selected
	// administrator recipients. The supplied usage is post-increment data, while
	// the dimension carries threshold configuration for rendering; recipient
	// storage remains stable during the send.
	SendQuotaAlertEmails([]string, int64, string, string, contract.QuotaDimension, float64, string)
}

// QuotaNotifyProvider combines provider display identity with its quota alert
// dimensions.
type QuotaNotifyProvider struct {
	ID int64 // alert provider record identifier
	// Name and Platform identify the provider in the selected alert. A successful
	// fresh-provider read replaces both values together with the dimensions used
	// for that check.
	// Name: provider display label; Platform: provider platform name.
	Name, Platform string
	Dimensions     []QuotaNotifyDimension // provider quota alert configurations
}

// QuotaNotifyReader supplies current provider quota and alert settings when
// committed usage is absent.
type QuotaNotifyReader interface {
	// GetByID returns current display and quota-alert data for the provider.
	// Successful reads provide a non-nil result; errors allow
	// BalanceNotifyService to retain its earlier snapshot for this check.
	GetByID(context.Context, int64) (*QuotaNotifyProvider, error)
}

// BalanceNotifyService selects post-charge alert events and dispatches their
// selected recipients.
type BalanceNotifyService struct {
	// emailService and settingRepo are required by both post-charge checks. If
	// either dependency is absent, those checks return before selecting
	// thresholds or recipients for dispatch.
	emailService AlertSender // billing alert delivery sender
	// settingRepo supplies global alert rules. Direct readers such as GetSiteName
	// and GetBalanceNotifyConfig require a callable store because they run
	// without the post-charge dependency checks.
	settingRepo NotifySettings // global alert setting reader
	// providerRepo is optional. Quota checks retain the supplied snapshot when
	// this reader is absent or reports an error.
	providerRepo QuotaNotifyReader // current provider quota reader
	// background determines whether send callbacks run immediately or later.
	// Dispatch callers supply a callable runner and retain captured input until
	// its callback finishes.
	background func(string, func()) // alert send dispatch runner
}

// NewBalanceNotifyService binds alert delivery, settings, provider reads and
// the background runner. Construction starts no work; dispatch requires a
// callable background runner, while provider refresh can use the supplied
// snapshot when its reader is absent.
func NewBalanceNotifyService(sender AlertSender, settings NotifySettings, providers QuotaNotifyReader, background func(string, func())) *BalanceNotifyService {
	return &BalanceNotifyService{emailService: sender, settingRepo: settings, providerRepo: providers, background: background}
}

// CheckBalanceAfterDeduction checks the supplied old balance and cost against
// effective user alert settings. A nil user, missing sender/settings, disabled
// user switch or unusable threshold suppresses dispatch. It computes the new
// balance from the supplied change; the runner must be callable when a
// crossing is selected. Notification delivery owns any later deduplication.
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

// CanNotifyBalance reports whether user enablement and the required balance
// alert services are present.
func (s *BalanceNotifyService) CanNotifyBalance(user *UserSummary) bool {
	if user == nil || s.emailService == nil || s.settingRepo == nil {
		return false
	}
	return user.BalanceNotifyEnabled
}

// ResolveUserEffectiveThreshold returns the user's effective balance trigger
// and global recharge link. The caller supplies a non-nil user and settings
// store; disabled global settings or a nonpositive selected threshold return
// ok=false.
func (s *BalanceNotifyService) ResolveUserEffectiveThreshold(ctx context.Context, user *UserSummary) (effectiveThreshold float64, rechargeURL string, ok bool) {
	globalEnabled, globalThreshold, rechargeURL := s.GetBalanceNotifyConfig(ctx)
	effective, ok := EffectiveBalanceThreshold(globalEnabled, globalThreshold, user.BalanceNotifyThreshold, user.BalanceNotifyThresholdType, user.TotalRecharged)
	if !ok {
		return 0, "", false
	}
	return effective, rechargeURL, true
}

// DispatchBalanceLowEmail reads branding and verified extra recipients, then
// submits the send callback to background. The caller supplies a non-nil user,
// sender, settings store and runner and keeps user identity fields unchanged
// until the callback finishes. Sender panics are logged inside that callback.
func (s *BalanceNotifyService) DispatchBalanceLowEmail(ctx context.Context, user *UserSummary, newBalance, threshold float64, rechargeURL string) {
	siteName := s.GetSiteName(ctx)
	recipients := s.CollectBalanceNotifyRecipients(user)
	slog.Info("CheckBalanceAfterDeduction: sending notification",
		"user_id", user.ID, "recipients", recipients, "new_balance", newBalance, "threshold", threshold)
	// Submit delivery after selecting settings and recipients; recovering inside
	// the callback keeps a sender panic from escaping that dispatched task.
	s.background("service/balance_notify_service.go:dispatchBalanceLowEmail", func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic in balance notification", "recover", r)
			}
		}()
		s.emailService.SendBalanceLowEmails(recipients, user.ID, user.Username, user.Email, newBalance, threshold, siteName, rechargeURL)
	})
}

// CheckProviderQuotaAfterIncrement selects alert crossings for a positive
// committed provider increment. Missing provider/sender/settings, a disabled
// global switch or absent verified administrator recipients suppresses this
// check. A selected crossing requires a callable runner. With quotaState,
// provider dimensions supply alert settings while the transaction result
// supplies usage and limits.
//
// Without committed state, the reader supplies fresh settings and usage, with
// the input snapshot retained on a failed or unavailable read. The prior usage
// is reconstructed from current usage minus cost; the supplied cost must match
// that observation in the same quota units.
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
		// Committed usage is unavailable, so read the current provider for both
		// dimensions and metadata; a read failure retains the original observation.
		freshProvider := s.FetchFreshProvider(ctx, provider)
		dims = append([]QuotaNotifyDimension(nil), freshProvider.Dimensions...)
		provider = freshProvider // use fresh data for alert metadata
	}
	s.CheckQuotaDimCrossings(provider, dims, cost, adminEmails, siteName)
}

// FetchFreshProvider returns the current provider or the supplied non-nil
// snapshot when its reader is absent or errors. A successful reader result is
// returned as supplied and must be non-nil for quota callers to read its
// dimensions.
func (s *BalanceNotifyService) FetchFreshProvider(ctx context.Context, snapshot *QuotaNotifyProvider) *QuotaNotifyProvider {
	if s.providerRepo == nil {
		return snapshot
	}
	fresh, err := s.providerRepo.GetByID(ctx, snapshot.ID)
	if err != nil {
		// Refresh failure leaves the supplied provider snapshot usable for this
		// alert decision; log the missing fresh observation before returning that
		// snapshot.
		slog.Warn("failed to fetch fresh provider for quota notify, using snapshot",
			"provider_id", snapshot.ID, "error", err)
		return snapshot
	}
	return fresh
}

// CheckQuotaDimCrossings submits one alert for each dimension crossing its
// effective usage trigger. provider must be non-nil, dimensions contain
// post-increment usage, and cost uses the same units. Selected crossings
// require a callable sender and runner; recipient slices remain unchanged
// until their background sends finish.
func (s *BalanceNotifyService) CheckQuotaDimCrossings(provider *QuotaNotifyProvider, dims []QuotaNotifyDimension, cost float64, adminEmails []string, siteName string) {
	for _, dim := range dims {
		if threshold, ok := dim.Crossing(cost); ok {
			s.AsyncSendQuotaAlert(adminEmails, provider.ID, provider.Name, provider.Platform, dim, dim.CurrentUsed, threshold, siteName)
		}
	}
}

// AsyncSendQuotaAlert submits a quota send callback and logs sender panics
// inside it. The caller provides a callable runner and sender and keeps
// adminEmails unchanged until delivery finishes. The sender receives dim and
// newUsed; effectiveThreshold is retained as an unused compatibility
// parameter.
func (s *BalanceNotifyService) AsyncSendQuotaAlert(adminEmails []string, providerID int64, providerName, platform string, dim QuotaNotifyDimension, newUsed, effectiveThreshold float64, siteName string) {
	// Submit delivery after selecting settings and recipients; recovering inside
	// the callback keeps a sender panic from escaping that dispatched task.
	s.background("service/balance_notify_service.go:asyncSendQuotaAlert", func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic in quota notification", "recover", r)
			}
		}()
		s.emailService.SendQuotaAlertEmails(adminEmails, providerID, providerName, platform, contract.QuotaDimension(dim), newUsed, siteName)
	})
}

// GetBalanceNotifyConfig reads the global switch, threshold and recharge URL
// in one settings request. Read failure disables alerts and clears the result;
// malformed threshold text becomes zero, and only the literal true enables the
// switch.
func (s *BalanceNotifyService) GetBalanceNotifyConfig(ctx context.Context) (enabled bool, threshold float64, rechargeURL string) {
	keys := []string{SettingKeyBalanceLowNotifyEnabled, SettingKeyBalanceLowNotifyThreshold, SettingKeyBalanceLowNotifyRechargeURL}
	settings, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		// A failed settings snapshot cannot establish enabled alert rules, so this
		// deduction suppresses delivery and returns an empty configuration.
		return false, 0, ""
	}
	enabled = settings[SettingKeyBalanceLowNotifyEnabled] == "true"
	if v := settings[SettingKeyBalanceLowNotifyThreshold]; v != "" {
		// Malformed thresholds leave zero, which the effective-threshold check
		// suppresses; a stored switch alone therefore cannot enable a usable balance
		// trigger.
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			threshold = f
		}
	}
	rechargeURL = settings[SettingKeyBalanceLowNotifyRechargeURL]
	return
}

// IsProviderQuotaNotifyEnabled reads the provider alert switch from the
// required settings store. The literal true enables alerts; a missing,
// different or unreadable value disables them for this observation.
func (s *BalanceNotifyService) IsProviderQuotaNotifyEnabled(ctx context.Context) bool {
	val, err := s.settingRepo.GetValue(ctx, SettingKeyProviderQuotaNotifyEnabled)
	if err != nil {
		// An unreadable provider switch supplies no enabled configuration, so the
		// completed increment produces no quota alert for this observation.
		return false
	}
	return val == "true"
}

// GetProviderQuotaNotifyEmails reads and filters administrator quota
// recipients from the required settings store. Read failure or absent/invalid
// saved contacts yields no recipients; verified enabled addresses are trimmed
// and deduplicated case-insensitively.
func (s *BalanceNotifyService) GetProviderQuotaNotifyEmails(ctx context.Context) []string {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyProviderQuotaNotifyEmails)
	if err != nil || strings.TrimSpace(raw) == "" || raw == "[]" {
		// Absent or unreadable recipient settings leave no verified administrator
		// destinations; suppress quota delivery while keeping the committed funds
		// operation intact.
		return nil
	}

	entries := ParseNotifyEmails(raw)
	if len(entries) == 0 {
		return nil
	}

	return FilterVerifiedEmails(entries)
}

// GetSiteName reads the configured alert branding from the required settings
// store. Missing or unreadable text uses TokenRouter; a supplied nonempty
// value is returned as stored, including surrounding whitespace.
func (s *BalanceNotifyService) GetSiteName(ctx context.Context) string {
	name, err := s.settingRepo.GetValue(ctx, SettingKeySiteName)
	if err != nil || name == "" {
		// A missing or failed branding read still permits alert rendering; choose
		// the built-in site name for the selected notification.
		return defaultSiteName
	}
	return name
}

// FilterVerifiedEmails returns verified enabled nonempty addresses in supplied
// order. Addresses are trimmed and deduplicated case-insensitively, with the
// first accepted spelling retained; input records remain available to the
// caller unchanged.
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

// CollectBalanceNotifyRecipients filters the non-nil user's saved extra alert
// addresses. Verification and disabled flags govern selection; the primary
// user email is passed separately to delivery as identity/display data.
func (s *BalanceNotifyService) CollectBalanceNotifyRecipients(user *UserSummary) []string {
	return FilterVerifiedEmails(user.BalanceNotifyExtraEmails)
}

// quotaDimsFromCommitted copies the provider dimensions and replaces daily,
// weekly and total usage/limits with committed transaction values. Both inputs
// must be non-nil; unknown dimension names retain their supplied values. Alert
// enablement, remaining threshold and threshold kind stay attached to each
// provider dimension.
//
// This combines configuration with the same transaction observation used for
// charging, so crossing reconstruction uses that committed increment instead
// of a later provider read. The copy lets these per-alert replacements leave
// the provider snapshot's dimension slice unchanged.
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
