package httpapi

import (
	"net/http"
	"strings"

	coderws "github.com/coder/websocket"
)

// ws_upgrade.go detects Responses WebSocket upgrades and closes connections
// using bounded reason strings.
// Responses upgrade detection reads the HTTP upgrade headers before choosing
// the WebSocket path. Close presentation bounds dynamic reason bytes before
// invoking the library handshake, then attempts immediate transport closure as
// well so a failed handshake does not skip cleanup.

// IsResponsesWSUpgrade checks upgrade intent in an HTTP request. Nil requests
// return false; Upgrade must equal websocket after trimming and case folding,
// while Connection uses a case-insensitive substring check for upgrade.
func IsResponsesWSUpgrade(r *http.Request) bool {
	if r == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return false
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(r.Header.Get("Connection"))), "upgrade")
}

// CloseResponsesWS attempts a close handshake with a trimmed reason capped at
// 120 bytes. Nil connections are ignored. It discards handshake errors and
// invokes immediate closure afterward; byte truncation can cut through a
// multibyte UTF-8 character.
func CloseResponsesWS(conn *coderws.Conn, status coderws.StatusCode, reason string) {
	if conn == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	// Cap dynamic close text below the control-frame payload budget.
	if len(reason) > 120 {
		reason = reason[:120]
	}
	// Attempt the protocol close first, then immediate closure even after
	// handshake failure so teardown does not depend on peer cooperation.
	_ = conn.Close(status, reason)
	_ = conn.CloseNow()
}
