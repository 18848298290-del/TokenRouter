package httpapi

import (
	"errors"
	"fmt"
	"strings"

	coderws "github.com/coder/websocket"

	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
)

// WebSocket diagnostics and termination attribution read two kinds of close
// evidence. Network-library close errors supply numeric log status, while
// gateway control errors retain the selected instruction and cancellation cause
// for the ingress classification path.

// SummarizeWSCloseErrorForLog returns the library close code and trimmed reason
// for diagnostics. Missing library close evidence yields two dashes; a blank
// reason keeps its dash even when a code is present. Gateway close instructions
// are read separately by ResponsesWSCloseInfo.
func SummarizeWSCloseErrorForLog(err error) (string, string) {
	if err == nil {
		return "-", "-"
	}
	statusCode := coderws.CloseStatus(err)
	if statusCode == -1 {
		return "-", "-"
	}
	closeStatus := fmt.Sprintf("%d(%s)", int(statusCode), statusCode.String())
	closeReason := "-"
	if closeErr, ok := errors.AsType[coderws.CloseError](err); ok {
		reason := strings.TrimSpace(closeErr.Reason)
		if reason != "" {
			closeReason = reason
		}
	}
	return closeStatus, closeReason
}

// ResponsesWSCloseInfo extracts the gateway-selected client close instruction
// through wrapped errors. Present is true only for OpenAIWSClientCloseError;
// other error types return empty information and may still contain
// network-library close evidence.
func ResponsesWSCloseInfo(err error) gatewayws.EntryClose {
	if closed, ok := errors.AsType[*OpenAIWSClientCloseError](err); ok {
		return gatewayws.EntryClose{Status: int(closed.StatusCode()), Reason: closed.Reason(), Present: true}
	}
	return gatewayws.EntryClose{}
}
