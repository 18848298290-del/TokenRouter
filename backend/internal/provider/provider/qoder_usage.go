package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// QoderUsage binds sessions and transport; the provider core owns usage caching and conditional health writes.
type QoderUsage struct {
	Sessions  *QoderTokenProvider
	Transport QoderTransport
	Profiles  *egressprovider.TLSProfiles
}

func (s *QoderUsage) Options() providercore.QoderUsageOptions {
	return providercore.QoderUsageOptions{
		Fetch: func(ctx context.Context, value *providercore.Record, now func() time.Time) (*providercore.UsageInfo, error) {
			response, err := s.fetchQoderQuotaUsage(ctx, value)
			if err != nil {
				return nil, err
			}
			return buildQoderUsageInfoAt(response, now()), nil
		},
		Degrade: buildQoderDegradedUsageAt,
	}
}

// qoderQuotaProgressFromRaw converts site quota progress into provider display data.
func qoderQuotaProgressFromRaw(raw *qoder.QuotaProgress, useCapAsTotal bool) *providercore.QoderQuotaProgress {
	value := qoder.NormalizeQuotaProgress(raw, useCapAsTotal)
	if value == nil {
		return nil
	}
	return &providercore.QoderQuotaProgress{
		Total:          value.Total,
		Used:           value.Used,
		Remaining:      value.Remaining,
		Percentage:     value.Percentage,
		Unit:           value.Unit,
		DetailURL:      value.DetailURL,
		Cap:            value.Cap,
		Available:      value.Available,
		OrganizationID: value.OrganizationID,
	}
}

func (s *QoderUsage) fetchQoderQuotaUsage(ctx context.Context, provider *providercore.Record) (*qoder.QuotaUsageResponse, error) {
	if provider == nil {
		return nil, fmt.Errorf("qoder: provider is nil")
	}
	sessionSource := s.Sessions
	if sessionSource == nil {
		sessionSource = NewQoderTokenProvider(qoder.SessionBuilder{})
		sessionSource.SetHTTPUpstream(s.Transport, s.Profiles)
	}
	usage, err := s.fetchQoderQuotaUsageWithProvider(ctx, provider, sessionSource)
	if err == nil || strings.TrimSpace(provider.GetCredential("pat")) == "" || !isQoderAuthenticationError(err) {
		return usage, err
	}

	// On PAT authentication failure, discard the cached session and retry once after exchanging it again.
	sessionSource.Invalidate(provider.ID)
	return s.fetchQoderQuotaUsageWithProvider(ctx, provider, sessionSource)
}

func (s *QoderUsage) fetchQoderQuotaUsageWithProvider(ctx context.Context, provider *providercore.Record, sessionSource *QoderTokenProvider,
) (*qoder.QuotaUsageResponse, error) {
	session, err := sessionSource.GetSession(ctx, provider)
	if err != nil {
		return nil, err
	}
	site, err := qoder.ParseSite(provider.GetCredential("site"))
	if err != nil {
		return nil, err
	}
	profile, err := qoder.ProfileForSite(site)
	if err != nil {
		return nil, err
	}
	logicalPath := qoder.QuotaUsagePath
	if profile.Site == qoder.SiteCN {
		query := url.Values{}
		if organizationID := strings.TrimSpace(session.Identity.OrganizationID); organizationID != "" {
			query.Set("orgId", organizationID)
		}
		if encoded := query.Encode(); encoded != "" {
			logicalPath += "?" + encoded
		}
	}
	doer := QoderRequestDoer(provider, s.Transport, s.Profiles)
	var usage qoder.QuotaUsageResponse
	client := qoder.NewClientForProfile(profile)
	request := client.BearerJSONRequestContextWithDoer
	if qoderQuotaUsesSignedAuth(provider, profile.Site) {
		request = client.JSONRequestContextWithDoer
	}
	if err := request(ctx, http.MethodGet, session, logicalPath, nil, nil, doer, &usage); err != nil {
		return nil, fmt.Errorf("qoder: quota usage request: %w", err)
	}
	return &usage, nil
}

// qoderQuotaUsesSignedAuth selects COSY signing for Global, CN PAT, and QoderCN20; legacy CN sessions use Bearer
// auth.
func qoderQuotaUsesSignedAuth(provider *providercore.Record, site qoder.Site) bool {
	if site != qoder.SiteCN {
		return true
	}
	if strings.TrimSpace(provider.GetCredential("pat")) != "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(provider.GetCredential("refresh_mode")), qoder.RefreshModeQoderCN20)
}

// isQoderAuthenticationError accepts only 401 and 403 as reasons to rebuild a PAT session.
func isQoderAuthenticationError(err error) bool {
	var apiErr *qoder.APIError
	return errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden)
}

func buildQoderUsageInfoAt(resp *qoder.QuotaUsageResponse, now time.Time) *providercore.UsageInfo {
	return &providercore.UsageInfo{
		Source:     "active",
		UpdatedAt:  &now,
		QoderQuota: qoderQuotaInfoFromResponse(resp, now, false),
	}
}

func qoderQuotaInfoFromResponse(resp *qoder.QuotaUsageResponse, updatedAt time.Time, fromSnapshot bool) *providercore.QoderQuotaInfo {
	if resp == nil {
		return nil
	}
	var expiresAt *time.Time
	if resp.ExpiresAt > 0 {
		rawExpiresAt := int64(resp.ExpiresAt)
		var t time.Time
		if rawExpiresAt >= 1_000_000_000_000 {
			t = time.UnixMilli(rawExpiresAt)
		} else {
			t = time.Unix(rawExpiresAt, 0)
		}
		expiresAt = &t
	}
	quota := &providercore.QoderQuotaInfo{
		UserID:               strings.TrimSpace(resp.UserID),
		UserType:             strings.TrimSpace(resp.UserType),
		UsageType:            strings.TrimSpace(resp.UsageType),
		TotalUsagePercentage: qoder.NormalizeQuotaPercentage(resp.TotalUsagePercentage),
		IsQuotaExceeded:      resp.IsQuotaExceeded,
		ExpiresAt:            expiresAt,
		UpgradeURL:           strings.TrimSpace(resp.UpgradeURL),
		AddCreditsURL:        strings.TrimSpace(resp.AddCreditsURL),
		IsPlanQuotaProrated:  resp.IsPlanQuotaProrated,
		LastUpdatedAt:        &updatedAt,
		SnapshotFromProvider: fromSnapshot,
	}
	quota.UserQuota = qoderQuotaProgressFromRaw(resp.UserQuota, true)
	quota.AddOnQuota = qoderQuotaProgressFromRaw(firstNonNilQoderQuotaProgress(resp.AddOnQuota, resp.AddOnQuotaSnake), true)
	quota.OrgResourcePackage = qoderQuotaProgressFromRaw(firstNonNilQoderQuotaProgress(
		resp.OrgResourcePackage,
		resp.OrgResourcePkgSnake,
		resp.SharedQuota,
		resp.SharedQuotaSnake,
	), true)
	return quota
}

func firstNonNilQoderQuotaProgress(values ...*qoder.QuotaProgress) *qoder.QuotaProgress {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func buildQoderDegradedUsageAt(err error, provider *providercore.Record, now time.Time) *providercore.UsageInfo {
	info := &providercore.UsageInfo{
		UpdatedAt: &now,
		Error:     fmt.Sprintf("usage API error: %v", err),
	}
	if err != nil {
		if apiErr, ok := errors.AsType[*qoder.APIError](err); ok {
			switch apiErr.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				info.ErrorCode = providercore.ErrorCodeUnauthenticated
				info.NeedsReauth = true
			case http.StatusTooManyRequests:
				info.ErrorCode = providercore.ErrorCodeRateLimited
			default:
				info.ErrorCode = providercore.ErrorCodeNetworkError
			}
			if snapshot := qoderQuotaSnapshotFromExtra(provider); snapshot != nil {
				snapshot.SnapshotFromProvider = true
				info.QoderQuota = snapshot
			}
			return info
		}
		errStr := err.Error()
		switch {
		case strings.Contains(errStr, "status 401") || strings.Contains(errStr, "status 403"):
			info.ErrorCode = providercore.ErrorCodeUnauthenticated
			info.NeedsReauth = true
		case strings.Contains(errStr, "status 429"):
			info.ErrorCode = providercore.ErrorCodeRateLimited
		case strings.Contains(errStr, "request:"):
			info.ErrorCode = providercore.ErrorCodeNetworkError
		default:
			info.ErrorCode = providercore.ErrorCodeNetworkError
		}
	}
	if snapshot := qoderQuotaSnapshotFromExtra(provider); snapshot != nil {
		snapshot.SnapshotFromProvider = true
		info.QoderQuota = snapshot
	}
	return info
}

func qoderQuotaSnapshotFromExtra(provider *providercore.Record) *providercore.QoderQuotaInfo {
	if provider == nil || provider.Extra == nil {
		return nil
	}
	raw, ok := provider.Extra[providercore.QoderUsageQuotaSnapshotExtraKey]
	if !ok || raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var quota providercore.QoderQuotaInfo
	if err := json.Unmarshal(data, &quota); err != nil {
		return nil
	}
	if quota.LastUpdatedAt == nil {
		if updatedRaw, ok := provider.Extra[providercore.QoderUsageQuotaUpdatedAtExtraKey].(string); ok {
			if parsed, err := time.Parse(time.RFC3339, updatedRaw); err == nil {
				quota.LastUpdatedAt = &parsed
			}
		}
	}
	return &quota
}
