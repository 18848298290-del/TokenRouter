package httpapi

import (
	"context"
	"encoding/json"

	"github.com/tidwall/gjson"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

func (s *OpenAITextExecutor) modelAttributes() *routing.ModelAttributeService {
	if s == nil || s.Requests == nil || s.Requests.Readers == nil {
		return nil
	}
	return s.Requests.Readers.Attributes
}

func (p *openAIMessagesExecutionAdapter) modelResolution(model string, request any, protocol string) (modelcatalog.Resolution, error) {
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
