package qoder

import (
	"fmt"
	"net"
	"runtime"
	"strings"
)

const (
	// SiteGlobal is the international site and the default for legacy providers.
	SiteGlobal Site = "global"
	// SiteCN is the China site.
	SiteCN Site = "cn"

	// RefreshModeCosy identifies credentials containing a COSY identity.
	RefreshModeCosy = "cosy"
	// RefreshModeQoderCN20 identifies credentials containing a QoderCN20 refresh token.
	RefreshModeQoderCN20 = "qodercn20"

	// GlobalDeviceAuthorizationURL is the global browser authorization URL.
	GlobalDeviceAuthorizationURL = "https://qoder.com/device/selectAccounts"
	// CNDeviceAuthorizationURL is the CN browser authorization URL.
	CNDeviceAuthorizationURL = "https://qoder.com.cn/device/selectAccounts"
	// GlobalOpenAPIBaseURL is the global OpenAPI base URL.
	GlobalOpenAPIBaseURL = "https://openapi.qoder.sh"
	// CNOpenAPIBaseURL is the CN OpenAPI base URL.
	CNOpenAPIBaseURL = "https://openapi.qoder.com.cn"
	// GlobalCenterBaseURL is the global Center base URL.
	GlobalCenterBaseURL = "https://center.qoder.sh"
	// GlobalGatewayBaseURL is the global inference gateway URL.
	GlobalGatewayBaseURL = "https://api1.qoder.sh"
	// CNGatewayBaseURL is the CN inference gateway URL.
	CNGatewayBaseURL = "https://gateway.qoder.com.cn"
	// GlobalClientVersion is the COSY version sent to the global site.
	GlobalClientVersion = "1.24.2"
	// CNClientVersion is the COSY version sent to the CN site.
	CNClientVersion = "1.24.2"
	// GlobalOAuthClientID is the global public OAuth client ID.
	GlobalOAuthClientID = "e883ade2-e6e3-4d6d-adf7-f92ceff5fdcb"
	// CNOAuthClientID is the CN public OAuth client ID.
	CNOAuthClientID = "f5a7f67c-11a8-491e-8b8e-a07f2d0df4b7"
	// GlobalOpenAPIProductName is the product name in the global OpenAPI User-Agent.
	GlobalOpenAPIProductName = "Qoder"
	// CNOpenAPIProductName is the product name in the CN OpenAPI User-Agent.
	CNOpenAPIProductName = "Qoder CN"
)

// Site identifies a Qoder provider site.
type Site string

// Profile holds the protocol endpoints and client identifiers for a site.
// Tests may copy the profile and override endpoints; production uses ProfileForSite defaults.
type Profile struct {
	Site                   Site
	DeviceAuthorizationURL string
	OpenAPIBaseURL         string
	CenterBaseURL          string
	GatewayBaseURL         string
	ClientVersion          string
	OAuthClientID          string
}

// ParseSite validates the site, treating empty values as global for legacy providers.
func ParseSite(value string) (Site, error) {
	switch Site(strings.ToLower(strings.TrimSpace(value))) {
	case "", SiteGlobal:
		return SiteGlobal, nil
	case SiteCN:
		return SiteCN, nil
	default:
		return "", fmt.Errorf("qoder: unsupported site %q", strings.TrimSpace(value))
	}
}

// ParseRefreshMode validates the mode, treating empty values as COSY refresh.
func ParseRefreshMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", RefreshModeCosy:
		return RefreshModeCosy, nil
	case RefreshModeQoderCN20:
		return RefreshModeQoderCN20, nil
	default:
		return "", fmt.Errorf("qoder: unsupported refresh_mode %q", strings.TrimSpace(value))
	}
}

// ProfileForSite returns a copy of the site defaults.
func ProfileForSite(site Site) (Profile, error) {
	parsed, err := ParseSite(string(site))
	if err != nil {
		return Profile{}, err
	}
	switch parsed {
	case SiteCN:
		return Profile{
			Site:                   SiteCN,
			DeviceAuthorizationURL: CNDeviceAuthorizationURL,
			OpenAPIBaseURL:         CNOpenAPIBaseURL,
			GatewayBaseURL:         CNGatewayBaseURL,
			ClientVersion:          CNClientVersion,
			OAuthClientID:          CNOAuthClientID,
		}, nil
	default:
		return Profile{
			Site:                   SiteGlobal,
			DeviceAuthorizationURL: GlobalDeviceAuthorizationURL,
			OpenAPIBaseURL:         GlobalOpenAPIBaseURL,
			CenterBaseURL:          GlobalCenterBaseURL,
			GatewayBaseURL:         GlobalGatewayBaseURL,
			ClientVersion:          GlobalClientVersion,
			OAuthClientID:          GlobalOAuthClientID,
		}, nil
	}
}

// MustProfileForSite returns the profile or panics for an invalid site. Use it with known site constants.
func MustProfileForSite(site Site) Profile {
	profile, err := ProfileForSite(site)
	if err != nil {
		panic(err)
	}
	return profile
}

// NormalizeProfile validates the site and fills fields omitted from an override profile.
func NormalizeProfile(profile Profile) (Profile, error) {
	base, err := ProfileForSite(profile.Site)
	if err != nil {
		return Profile{}, err
	}
	if strings.TrimSpace(profile.DeviceAuthorizationURL) != "" {
		base.DeviceAuthorizationURL = strings.TrimRight(strings.TrimSpace(profile.DeviceAuthorizationURL), "/")
	}
	if strings.TrimSpace(profile.OpenAPIBaseURL) != "" {
		base.OpenAPIBaseURL = strings.TrimRight(strings.TrimSpace(profile.OpenAPIBaseURL), "/")
	}
	if strings.TrimSpace(profile.CenterBaseURL) != "" {
		base.CenterBaseURL = strings.TrimRight(strings.TrimSpace(profile.CenterBaseURL), "/")
	}
	if strings.TrimSpace(profile.GatewayBaseURL) != "" {
		base.GatewayBaseURL = strings.TrimRight(strings.TrimSpace(profile.GatewayBaseURL), "/")
	}
	if strings.TrimSpace(profile.ClientVersion) != "" {
		base.ClientVersion = strings.TrimSpace(profile.ClientVersion)
	}
	if strings.TrimSpace(profile.OAuthClientID) != "" {
		base.OAuthClientID = strings.TrimSpace(profile.OAuthClientID)
	}
	return base, nil
}

// OpenAPIUserAgent returns the site client User-Agent for OpenAPI requests.
func (p Profile) OpenAPIUserAgent() string {
	normalized, err := NormalizeProfile(p)
	if err != nil {
		normalized = MustProfileForSite(SiteGlobal)
	}
	productName := GlobalOpenAPIProductName
	if normalized.Site == SiteCN {
		productName = CNOpenAPIProductName
	}
	return productName + "/" + normalized.ClientVersion
}

// MachineOS returns the COSY architecture_system identifier.
func MachineOS() string {
	return MachineOSFor(runtime.GOARCH, runtime.GOOS)
}

// MachineOSFor normalizes Go platform names to Qoder protocol names.
func MachineOSFor(arch, goos string) string {
	arch = strings.ToLower(strings.TrimSpace(arch))
	goos = strings.ToLower(strings.TrimSpace(goos))
	switch arch {
	case "amd64", "x86_64":
		arch = "x86_64"
	case "arm64", "aarch64":
		arch = "aarch64"
	}
	return arch + "_" + goos
}

// MachineIP returns the first non-loopback IPv4 address for Cosy-ClientIp, or an empty string.
func MachineIP() string {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	return machineIPFromAddresses(addresses)
}

func machineIPFromAddresses(addresses []net.Addr) string {
	for _, address := range addresses {
		ipNet, ok := address.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		if ipv4 := ipNet.IP.To4(); ipv4 != nil {
			return ipv4.String()
		}
	}
	return ""
}
