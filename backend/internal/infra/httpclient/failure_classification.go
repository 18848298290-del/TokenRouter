package httpclient

import (
	"errors"
	"net"
	"strings"
	"syscall"
)

// Transport errors can provide wrapped errno or DNS details, or only a
// proxy's diagnostic text. Classification gives routing callers a
// persistent-failure indication from those available signals; callers apply
// their own provider removal and retry policy.

// TransportFailure classifies technical failures; callers decide provider removal and retries.
type TransportFailure struct {
	// Persistent identifies a recognized connection, DNS, or proxy-authentication
	// failure; routing callers still decide whether to remove or retry providers.
	Persistent bool
}

// ClassifyTransportFailure reports whether err matches refused or unreachable
// connections, missing DNS names, or selected proxy-authentication text. Nil
// and unrecognized errors return an unset classification; string markers are
// matched case-insensitively after typed checks.
func ClassifyTransportFailure(err error) TransportFailure {
	if err == nil {
		return TransportFailure{}
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return TransportFailure{Persistent: true}
	}
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) && dnsError.IsNotFound {
		return TransportFailure{Persistent: true}
	}
	// Proxy wrappers may expose diagnostic text without an errno or DNS type.
	// Recognize the selected failure messages after typed classification has missed.
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"authentication failed", "proxy authentication required", "connection refused",
		"no route to host", "network is unreachable", "no such host",
	} {
		if strings.Contains(message, marker) {
			return TransportFailure{Persistent: true}
		}
	}
	return TransportFailure{}
}
