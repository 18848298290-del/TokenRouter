package qoder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/pkg/requestcontext"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
)

// StreamClient sends a prepared Qoder request.
type StreamClient interface {
	StreamRequestContext(context.Context, *SessionContext, string, []byte, map[string]string) (*http.Response, error)
}
type streamClientWithDoer interface {
	StreamRequestContextWithDoer(context.Context, *SessionContext, string, []byte, map[string]string, RequestDoer) (*http.Response, error)
}

// Target holds the request site, metadata, and session and client factories.
type Target struct {
	Attributes func(context.Context, string, protocol.ProtocolID) (modelcatalog.Resolution, error)
	ProviderID int64
	Site       Site
	UserType   string
	Metadata   RequestMetadata
	Session    func(context.Context) (*SessionContext, error)
	Client     func() (StreamClient, error)
	Doer       RequestDoer
}

// ExecuteOptions configures conversation storage, the execution timeout, and activity registration.
type ExecuteOptions struct {
	Conversations *QoderConversationStore
	Timeout       time.Duration
	Enter         func() (func(), error)
}
type Executor struct {
	conversations *QoderConversationStore
	timeout       time.Duration
	enter         func() (func(), error)
}

func (t *Target) TargetID() int64 {
	if t == nil {
		return 0
	}
	return t.ProviderID
}

// String reports the provider ID without exposing credentials or transport details.
func (t *Target) String() string { return fmt.Sprintf("qoder target provider=%d", t.TargetID()) }

func NewExecutor(options ExecuteOptions) *Executor {
	if options.Conversations == nil {
		options.Conversations = NewQoderConversationStore(QoderConversationTTL)
	}
	if options.Timeout <= 0 {
		options.Timeout = QoderStreamTimeout
	}
	return &Executor{conversations: options.Conversations, timeout: options.Timeout, enter: options.Enter}
}

// PrepareConversation builds the request plan; Execute commits acceptance and completion.
func (e *Executor) PrepareConversation(target *Target, wireProtocol protocol.ProtocolID, request QoderPayloadRequest) (map[string]any, string, *QoderConversationPlan) {
	request.UserType = target.UserType
	request.Site = target.Site
	key, source := QoderConversationKey(target.Metadata, target.ProviderID, string(wireProtocol), request)
	plan := e.conversations.PlanWithOptions(key, request.System, request.Tools, request.Messages, QoderConversationPlanOptions{AppendToExisting: wireProtocol == protocol.ProtocolOpenAIResponses && strings.TrimSpace(request.PreviousResponseID) != ""})
	payload, model := BuildQoderPayloadWithOptions(request, plan.SessionID, plan.MessagesToSend, plan.IncludeSystem, plan.IncludeTools)
	plan.Log(target.Metadata, target.ProviderID, string(wireProtocol), request.Model, source, request, payload)
	return payload, model, plan
}

// Execute serves Chat, Messages, and Responses and retains observed partial usage on stream errors.
// @project-doc docs/interfaces/qoder_upstream.md#qoder_execution_boundary
func (e *Executor) Execute(ctx context.Context, input upstream.AttemptInput, sink upstream.OutputSink) (result upstream.AttemptResult, executionErr error) {
	if e.enter != nil {
		done, err := e.enter()
		if err != nil {
			return upstream.AttemptResult{}, err
		}
		defer done()
	}
	start := time.Now()
	observed := &executionOutput{sink: sink, started: start}
	sink = observed
	defer func() {
		result.ClientDisconnect = result.ClientDisconnect || observed.disconnected
		result.FirstSemanticOutput = observed.firstSemantic
		if ctx != nil && ctx.Err() != nil {
			result.Cancelled = true
			result.ClientDisconnect = result.ClientDisconnect || result.Stream
		}
		if executionErr != nil {
			switch {
			case errors.Is(executionErr, context.Canceled):
				result.FailureClass = "cancelled"
			case errors.Is(executionErr, context.DeadlineExceeded):
				result.FailureClass = "timeout"
			default:
				result.FailureClass = "upstream"
			}
		}
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return upstream.AttemptResult{}, err
	}
	target, ok := input.Target.(*Target)
	if !ok || target == nil || target.Session == nil || target.Client == nil {
		return upstream.AttemptResult{}, errors.New("qoder gateway service is not configured")
	}
	executionCtx := ctx
	if input.Stream {
		executionCtx = requestcontext.Detach(ctx)
	}
	executionCtx, cancel := context.WithTimeout(executionCtx, e.timeout)
	defer cancel()
	var request QoderPayloadRequest
	var err error
	switch input.Protocol {
	case protocol.ProtocolOpenAIChatCompletions:
		request, err = ParseQoderChatCompletionsPayload(input.Body)
	case protocol.ProtocolOpenAIResponses:
		request, err = ParseQoderResponsesPayload(input.Body)
	case protocol.ProtocolAnthropicMessages:
		request, err = ParseQoderAnthropicMessagesPayload(input.Body)
	default:
		return upstream.AttemptResult{}, errors.New("unsupported qoder client protocol")
	}
	if err != nil {
		return upstream.AttemptResult{}, err
	}
	responseModel := input.ResponseModel
	if responseModel == "" {
		responseModel = request.Model
	}
	if target.Attributes != nil {
		modelKey := ResolveQoderModelForSite(target.Site, request.Model).Key
		attributes, resolveErr := target.Attributes(ctx, modelKey, input.Protocol)
		if resolveErr != nil {
			return upstream.AttemptResult{}, resolveErr
		}
		request.Capabilities = &attributes
	}
	payload, model, plan := e.PrepareConversation(target, input.Protocol, request)
	body, err := json.Marshal(payload)
	if err != nil {
		return upstream.AttemptResult{}, fmt.Errorf("marshal qoder payload: %w", err)
	}
	// Credential waits follow request cancellation. Started streams collect trailing usage within the execution timeout.
	if err := ctx.Err(); err != nil {
		return upstream.AttemptResult{}, err
	}
	session, err := target.Session(ctx)
	if err != nil {
		return upstream.AttemptResult{}, fmt.Errorf("get qoder session: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return upstream.AttemptResult{}, err
	}
	client, err := target.Client()
	if err != nil {
		return upstream.AttemptResult{}, err
	}
	headers := map[string]string{"x-model-key": model, "x-model-source": "system"}
	if err := ctx.Err(); err != nil {
		return upstream.AttemptResult{}, err
	}
	var response *http.Response
	if doerClient, ok := client.(streamClientWithDoer); ok && target.Doer != nil {
		response, err = doerClient.StreamRequestContextWithDoer(executionCtx, session, "", body, headers, target.Doer)
	} else {
		response, err = client.StreamRequestContext(executionCtx, session, "", body, headers)
	}
	if err != nil {
		return upstream.AttemptResult{}, err
	}
	plan.CommitAccepted()
	result = upstream.AttemptResult{Model: responseModel, UpstreamModel: model, Stream: input.Stream, UpstreamHeaders: response.Header.Clone()}
	if input.Protocol == protocol.ProtocolOpenAIResponses {
		result.RequestID = request.ResponseID
	}
	output := upstream.NewOutputContext(sink)
	mapper := QoderDeclaredToolNameMapper(QoderAnySlice(payload["tools"]))
	var nativeUsage upstream.TokenUsage
	commitComplete := true
	if input.Stream {
		var stream *QoderStreamResult
		switch input.Protocol {
		case protocol.ProtocolOpenAIChatCompletions:
			stream, err = WriteQoderOpenAIStreamResponse(executionCtx, output, responseModel, response, QoderOpenAIStreamUsageMapper(plan.RecordUsage), QoderOpenAIStreamToolNameMapper(mapper), QoderOpenAIStreamIncludeUsage(GjsonBool(input.Body, "stream_options.include_usage")))
		case protocol.ProtocolOpenAIResponses:
			stream, err = WriteQoderResponsesStreamResponse(executionCtx, output, responseModel, response, QoderResponsesStreamUsageMapper(plan.RecordUsage), QoderResponsesStreamToolNameMapper(mapper), QoderResponsesStreamResponseID(request.ResponseID))
		case protocol.ProtocolAnthropicMessages:
			stream, err = WriteQoderAnthropicStreamResponse(executionCtx, output, responseModel, response, QoderAnthropicStreamUsageMapper(plan.RecordUsage), QoderAnthropicStreamToolNameMapper(mapper))
		}
		if stream != nil {
			nativeUsage = stream.Usage
			result.Usage = plan.RecordUsage(nativeUsage)
			result.HasUsage = stream.HasUsage
			result.Served = stream.HasOutput
			commitComplete = stream.HasOutput
		}
		if err != nil {
			plan.RollbackAccepted()
			result.Duration = time.Since(start)
			return result, err
		}
	} else {
		events, readErr := ReadQoderSSEEventsContext(executionCtx, response, nil)
		if readErr != nil {
			plan.RollbackAccepted()
			return upstream.AttemptResult{}, readErr
		}
		nativeUsage = QoderUsageFromEvents(events)
		result.Usage = plan.RecordUsage(nativeUsage)
		for _, event := range events {
			result.HasUsage = result.HasUsage || event.HasUsage
		}
		var responseBody []byte
		switch input.Protocol {
		case protocol.ProtocolOpenAIChatCompletions:
			responseBody, err = BuildQoderOpenAICompletion(responseModel, QoderEventsWithUsage(events, nativeUsage), mapper)
		case protocol.ProtocolOpenAIResponses:
			responseBody, err = BuildQoderResponsesResponseWithID(responseModel, request.ResponseID, QoderEventsWithUsage(events, result.Usage), mapper)
		case protocol.ProtocolAnthropicMessages:
			responseBody, err = BuildQoderAnthropicMessage(responseModel, QoderEventsWithUsage(events, nativeUsage), mapper)
		}
		if err != nil {
			plan.RollbackAccepted()
			return upstream.AttemptResult{}, err
		}
		result.Served = true
		// Completed upstream work remains billable when the non-streaming response write fails.
		if writeErr := sink.Begin(upstream.OutputHead{Status: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}}); writeErr != nil {
			result.ClientDisconnect = true
		} else if writeErr = sink.Emit(upstream.OutputEvent{Data: responseBody, Semantic: true, CommitForRetry: true, Terminal: true}); writeErr != nil {
			result.ClientDisconnect = true
		}
	}
	plan.LogUsage(target.Metadata, target.ProviderID, nativeUsage, result.Usage)
	if commitComplete {
		plan.Commit(nativeUsage)
	}
	if input.Protocol == protocol.ProtocolOpenAIResponses && request.ResponseID != "" {
		plan.AddAlias(QoderProviderScopedConversationKey(target.ProviderID, QoderConversationExplicitSessionKey(target.Metadata, request.ResponseID)))
	}
	result.Duration = time.Since(start)
	return result, nil
}
