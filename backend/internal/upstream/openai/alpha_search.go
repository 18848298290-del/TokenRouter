package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// AlphaSearchTarget holds a prepared request and its execution callbacks.
// The request may contain credentials; do not serialize or expand it in logs.
type AlphaSearchTarget struct {
	ProviderID        int64
	Request           *http.Request `json:"-"`
	ResponsesFallback bool
	Model             string
	Enter             func() (func(), error)
	Do                func(*http.Request) (*http.Response, error)
	Latency           func(time.Duration)
	TransportError    func(error) error
	ReadBody          func(io.Reader) ([]byte, error)
	HTTPError         func(*http.Response, []byte) error
	UpdateQuota       func(http.Header)
	Headers           func(http.Header, http.Header)
}

// AlphaSearchExecutor executes standalone search or the PAT Responses fallback,
// with separate error handling and quota-update rules for each path.
type AlphaSearchExecutor struct{}

func (t *AlphaSearchTarget) TargetID() int64 {
	if t == nil {
		return 0
	}
	return t.ProviderID
}

func (t *AlphaSearchTarget) String() string {
	return fmt.Sprintf("openai alpha search target provider=%d", t.TargetID())
}
func (t *AlphaSearchTarget) GoString() string { return t.String() }

func (AlphaSearchExecutor) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (result upstream.AttemptResult, failure error) {
	t, ok := input.Target.(*AlphaSearchTarget)
	if !ok || t == nil || t.Request == nil {
		return result, errors.New("openai alpha search target is not configured")
	}
	if input.Protocol != protocol.ProtocolAlphaSearch {
		return result, errors.New("unsupported alpha search protocol")
	}
	if t.Enter != nil {
		done, err := t.Enter()
		if err != nil {
			return result, err
		}
		defer done()
	}
	started := time.Now()
	resp, err := t.Do(t.Request)
	t.Latency(time.Since(started))
	if err != nil {
		return result, t.TransportError(err)
	}
	// Error handling may replace resp.Body for replay; close the body that owns the connection.
	originalBody := resp.Body
	defer func() { _ = originalBody.Close() }()
	// Measure duration before closing the body and record the returned failure.
	defer func() {
		result.Duration = time.Since(started)
		result.Cancelled = errors.Is(failure, context.Canceled) || errors.Is(failure, context.DeadlineExceeded)
		if failure != nil {
			result.FailureClass = "upstream"
		}
	}()
	body, err := t.ReadBody(resp.Body)
	if err != nil {
		if t.ResponsesFallback {
			return result, fmt.Errorf("read alpha search responses fallback response: %w", err)
		}
		return result, fmt.Errorf("read alpha search response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		if err := t.HTTPError(resp, body); err != nil {
			return result, err
		}
	}
	output := upstream.NewDeferredOutputContext(sink)
	success := resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices
	if !t.ResponsesFallback || success {
		t.UpdateQuota(resp.Header)
	}
	if t.ResponsesFallback && success {
		converted, err := OpenAIAlphaSearchResponseFromResponsesSSE(body)
		if err != nil {
			return result, err
		}
		output.Data(http.StatusOK, "application/json", converted)
	} else {
		t.Headers(output.Writer.Header(), resp.Header)
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/json"
		}
		output.Data(resp.StatusCode, contentType, body)
	}
	result.HTTPCommitted = output.Writer.Written()
	if !success {
		return result, nil
	}
	result.RequestID = strings.TrimSpace(resp.Header.Get("x-request-id"))
	result.UpstreamHeaders = resp.Header
	result.Model = input.ResponseModel
	result.UpstreamModel = t.Model
	result.SearchCount = 1
	result.Served = true
	return result, nil
}
