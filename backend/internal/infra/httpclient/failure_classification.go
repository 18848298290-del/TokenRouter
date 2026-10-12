package httpclient

import (
	"errors"
	"net"
	"strings"
	"syscall"
)

// TransportFailure classifies technical failures; callers decide provider removal and retries.
type TransportFailure struct {
	Persistent bool
}

// ClassifyTransportFailure identifies persistent failures from errno, DNS, and proxy error text.
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
