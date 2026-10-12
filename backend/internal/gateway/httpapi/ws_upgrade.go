package httpapi

import (
	"net/http"
	"strings"

	coderws "github.com/coder/websocket"
)

// IsResponsesWSUpgrade reports whether Upgrade is websocket and Connection
// contains "upgrade", using case-insensitive comparisons.
func IsResponsesWSUpgrade(r *http.Request) bool {
	if r == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return false
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(r.Header.Get("Connection"))), "upgrade")
}

// CloseResponsesWS trims reason to 120 bytes, attempts a close handshake,
// and closes the connection even if the handshake fails.
func CloseResponsesWS(conn *coderws.Conn, status coderws.StatusCode, reason string) {
	if conn == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > 120 {
		reason = reason[:120]
	}
	_ = conn.Close(status, reason)
	_ = conn.CloseNow()
}
