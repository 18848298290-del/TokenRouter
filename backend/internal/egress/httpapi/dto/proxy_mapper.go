package dto

import (
	"github.com/TokenFlux/TokenRouter/internal/egress"
)

// Proxy response mapping chooses whether credentials are visible through
// ordinary or administrator response types. Scalar values are copied, while
// optional expiration, backup, notes, and cached numeric pointers retain source
// ownership until the resulting response has been serialized.

// ProxyFromEgress maps a proxy into ordinary response metadata and leaves the
// JSON-excluded password unset. Nil input returns nil; expiration and backup
// pointers remain shared with the source and response consumers leave them
// immutable.
func ProxyFromEgress(p *egress.Proxy) *Proxy {
	if p == nil {
		return nil
	}
	return &Proxy{
		ID:             p.ID,
		Name:           p.Name,
		Protocol:       p.Protocol,
		Host:           p.Host,
		Port:           p.Port,
		Username:       p.Username,
		Status:         p.Status,
		CreatedAt:      p.CreatedAt,
		UpdatedAt:      p.UpdatedAt,
		ExpiresAt:      p.ExpiresAt,
		FallbackMode:   p.FallbackMode,
		BackupProxyID:  p.BackupProxyID,
		ExpiryWarnDays: p.ExpiryWarnDays,
	}
}

// ProxyFromEgressAdmin maps a proxy to credential-bearing administrator
// response data. Nil input returns nil, and the response retains optional
// source pointers through the base mapping; callers protect access before
// exposing the result.
func ProxyFromEgressAdmin(p *egress.Proxy) *AdminProxy {
	if p == nil {
		return nil
	}
	base := ProxyFromEgress(p)
	if base == nil {
		return nil
	}
	return &AdminProxy{
		Proxy:    *base,
		Password: p.Password,
	}
}

// ProxyWithProviderCountFromEgressAdmin maps administrator proxy metadata,
// provider counts, and cached probe values. Nil input returns nil; expiration,
// backup, and optional numeric observation pointers remain shared and consumers
// keep them stable through serialization.
func ProxyWithProviderCountFromEgressAdmin(p *egress.ProxyWithProviderCount) *AdminProxyWithProviderCount {
	if p == nil {
		return nil
	}
	admin := ProxyFromEgressAdmin(&p.Proxy)
	if admin == nil {
		return nil
	}
	return &AdminProxyWithProviderCount{
		AdminProxy:     *admin,
		ProviderCount:  p.ProviderCount,
		LatencyMs:      p.LatencyMs,
		LatencyStatus:  p.LatencyStatus,
		LatencyMessage: p.LatencyMessage,
		IPAddress:      p.IPAddress,
		Country:        p.Country,
		CountryCode:    p.CountryCode,
		Region:         p.Region,
		City:           p.City,
		QualityStatus:  p.QualityStatus,
		QualityScore:   p.QualityScore,
		QualityGrade:   p.QualityGrade,
		QualitySummary: p.QualitySummary,
		QualityChecked: p.QualityChecked,
	}
}

// ProxyProviderSummaryFromEgress maps bound-provider identity and notes into
// administrator response values. Nil input returns nil, and the optional Notes
// pointer remains shared with the source until response serialization
// completes.
func ProxyProviderSummaryFromEgress(a *egress.ProxyProviderSummary) *ProxyProviderSummary {
	if a == nil {
		return nil
	}
	return &ProxyProviderSummary{
		ID:       a.ID,
		Name:     a.Name,
		Platform: a.Platform,
		Type:     a.Type,
		Notes:    a.Notes,
	}
}
