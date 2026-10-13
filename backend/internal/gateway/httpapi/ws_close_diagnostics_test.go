package httpapi

import (
	"context"
	"errors"
	"fmt"
	"testing"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// TestOpenAIWSIngressEndedByClient_MatchesCloseCodeReportedInLog includes bare,
// wrapped and gateway control errors in close diagnostics. Whenever logging
// reports normal closure, termination classification must attribute the
// connection to the client.
func TestOpenAIWSIngressEndedByClient_MatchesCloseCodeReportedInLog(t *testing.T) {
	errs := []error{
		coderws.CloseError{Code: coderws.StatusNormalClosure, Reason: "client done"},
		fmt.Errorf("ingress turn 3: %w", coderws.CloseError{Code: coderws.StatusNormalClosure}), NewOpenAIWSClientCloseError(coderws.StatusNormalClosure, "websocket idle timeout", context.DeadlineExceeded), NewOpenAIWSClientCloseError(coderws.StatusGoingAway, "websocket request canceled", context.Canceled),
		coderws.CloseError{Code: coderws.StatusAbnormalClosure, Reason: "connection reset"},
		errors.New("upstream websocket read failed"),
	}

	for _, err := range errs {
		t.Run(err.Error(), func(t *testing.T) {
			closeStatus, _ := SummarizeWSCloseErrorForLog(err)
			if closeStatus == "1000(StatusNormalClosure)" {
				require.True(t, ResponsesWSEndedByClient(err, ResponsesWSCloseInfo(err)),
					"日志按 1000 归类为正常关闭，归因侧不得同时判为提供商故障")
			}
		})
	}
}
