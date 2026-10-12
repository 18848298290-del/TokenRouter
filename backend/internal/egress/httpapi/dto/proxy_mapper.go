package dto

import (
	"github.com/TokenFlux/TokenRouter/internal/egress"
)

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

// ProxyFromEgressAdmin converts a service Proxy to AdminProxy DTO for admin users.
// The response includes the password and requires administrator access.
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

// ProxyWithProviderCountFromEgressAdmin converts a service ProxyWithProviderCount to AdminProxyWithProviderCount DTO.
// The response includes the password and requires administrator access.
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
