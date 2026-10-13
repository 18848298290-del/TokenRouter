package httpapi

import (
	"context"
	"encoding/json"

	"github.com/tidwall/gjson"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// Model attributes are resolved against the selected execution provider and the
// request's protocol before platform conversion. Request-sensitive resolution
// can also inspect serialized parameters, while missing attribute readers let
// the provider's resolution helpers use their own catalog behavior.

// modelAttributes returns the configured text-execution model attribute reader,
// if available.
func (s *OpenAITextExecutor) modelAttributes() *routing.ModelAttributeService {
	if s == nil || s.Requests == nil || s.Requests.Readers == nil {
		return nil
	}
	return s.Requests.Readers.Attributes
}

// modelResolution resolves provider capabilities and adjusts them for
// serialized request parameters. It uses the HTTP context when available,
// otherwise background context. Successful resolution updates the execution
// provider before serialization, which can still fail.
func (p *openAIMessagesExecutionAdapter) modelResolution(model string, request any, protocol string) (modelcatalog.Resolution, error) {
	// Adapters without an HTTP request still resolve catalog attributes; a
	// background context gives those direct execution paths a usable lookup
	// context.
	ctx := context.Background()
	if p.c != nil && p.c.Request != nil {
		ctx = p.c.Request.Context()
	}
	result, err := gatewayprovider.ResolveModelAttributes(ctx, p.provider, model, p.s.modelAttributes(), protocol)
	if err != nil {
		return modelcatalog.Resolution{}, err
	}
	if p.provider != nil {
		p.provider.Capabilities = &result
	}
	body, err := json.Marshal(request)
	if err != nil {
		return modelcatalog.Resolution{}, err
	}
	return result.ForRequest(gatewayprovider.ModelRequestState(body)), nil
}

// applyModelAttributes prepares request parameters for the selected model and
// protocol candidates. A body without a model is returned unchanged. Missing
// Readers passes a nil attribute service to the provider helper; its
// preparation errors reach the caller.
func (s *OpenAIRequests) applyModelAttributes(ctx context.Context, value *gatewayprovider.ExecutionProvider, body []byte, protocols ...string) ([]byte, error) {
	var attributes *routing.ModelAttributeService
	if s != nil && s.Readers != nil {
		attributes = s.Readers.Attributes
	}
	model := gjson.GetBytes(body, "model").String()
	if model == "" {
		return body, nil
	}
	return gatewayprovider.PrepareModelAttributeParameters(ctx, value, model, body, attributes, protocols...)
}

// resolveModelAttributes installs resolved image-model capabilities on the
// execution provider. The caller supplies a nonnil provider. Resolution uses
// the optional request attribute reader, and an error leaves the capability
// pointer unassigned by this helper.
func (s *OpenAIImagesExecutor) resolveModelAttributes(ctx context.Context, value *gatewayprovider.ExecutionProvider, model string) error {
	var service *routing.ModelAttributeService
	if s.Requests != nil && s.Requests.Readers != nil {
		service = s.Requests.Readers.Attributes
	}
	resolved, err := gatewayprovider.ResolveModelAttributes(ctx, value, model, service)
	if err != nil {
		return err
	}
	value.Capabilities = &resolved
	return nil
}
