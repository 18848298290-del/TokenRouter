package httpapi

import (
	"errors"
	"fmt"
	"strings"

	coderws "github.com/coder/websocket"

	gatewayws "github.com/TokenFlux/TokenRouter/internal/gateway/ws"
)

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

// ResponsesWSCloseInfo extracts the status and reason from a client close error.
func ResponsesWSCloseInfo(err error) gatewayws.EntryClose {
	if closed, ok := errors.AsType[*OpenAIWSClientCloseError](err); ok {
		return gatewayws.EntryClose{Status: int(closed.StatusCode()), Reason: closed.Reason(), Present: true}
	}
	return gatewayws.EntryClose{}
}
