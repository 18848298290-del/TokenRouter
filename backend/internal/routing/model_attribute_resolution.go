package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

type modelAttributeContextKey struct{}

// ModelAttributeMatch restricts overrides to matching routes; empty fields match any value.
type ModelAttributeMatch struct {
	Platform         string `json:"platform,omitempty"`
	ProviderType     string `json:"provider_type,omitempty"`
	ProviderMode     string `json:"provider_mode,omitempty"`
	Site             string `json:"site,omitempty"`
	ClientProtocol   string `json:"client_protocol,omitempty"`
	UpstreamProtocol string `json:"upstream_protocol,omitempty"`
}

type attributeGroupResult struct {
	config      *ModelAttributeConfig
	err         error
	resolutions map[modelcatalog.TargetKey]modelcatalog.Resolution
}

// ModelAttributeSession pins the catalogue and caches group configuration reads across a request's attempts.
type ModelAttributeSession struct {
	service *ModelAttributeService
	view    modelcatalog.View
	mu      sync.Mutex
	groups  map[int64]attributeGroupResult
}

func (m *ModelAttributeMatch) Matches(target modelcatalog.Target) bool {
	if m == nil {
		return true
	}
	for _, pair := range [][2]string{{m.Platform, target.Platform}, {m.ProviderType, target.ProviderType}, {m.ProviderMode, target.ProviderMode}, {m.Site, target.Site}, {m.ClientProtocol, target.ClientProtocol}, {m.UpstreamProtocol, target.UpstreamProtocol}} {
		if pair[0] != "" && pair[0] != pair[1] {
			return false
		}
	}
	return true
}

func (m *ModelAttributeMatch) Validate() error {
	if m == nil {
		return nil
	}
	if m.Platform != "" && !slices.Contains([]string{"openai", "anthropic", "gemini", "antigravity", "grok", "qoder", "kimi", "zhipu", "deepseek", "jev"}, m.Platform) {
		return fmt.Errorf("unknown platform %q", m.Platform)
	}
	if m.ProviderType != "" && !slices.Contains([]string{"apikey", "oauth", "setup-token", "bedrock", "service_account", "cosy", "upstream"}, m.ProviderType) {
		return fmt.Errorf("unknown provider type %q", m.ProviderType)
	}
	if m.ProviderMode != "" && m.ProviderMode != "payg" && m.ProviderMode != "coding" {
		return fmt.Errorf("provider mode must be payg or coding")
	}
	if m.Site != "" && m.Site != "cn" && m.Site != "global" {
		return fmt.Errorf("site must be cn or global")
	}
	for _, value := range []string{m.ClientProtocol, m.UpstreamProtocol} {
		if value == "" {
			continue
		}
		found := false
		for _, item := range capability.ProtocolCatalog() {
			if string(item.ID) == value {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown protocol %q", value)
		}
	}
	return nil
}

func (s *ModelAttributeService) view() modelcatalog.View {
	if s != nil && s.Catalog.View != nil {
		return s.Catalog.View()
	}
	return modelcatalog.View{}
}

func (s *ModelAttributeService) viewForContext(ctx context.Context) modelcatalog.View {
	if session := ModelAttributesFromContext(ctx); session != nil && session.service == s {
		return session.view
	}
	return s.view()
}

func (s *ModelAttributeService) NewSession() *ModelAttributeSession {
	return &ModelAttributeSession{service: s, view: s.view(), groups: map[int64]attributeGroupResult{}}
}

// WithModelAttributes attaches a request catalogue snapshot. Call it separately for each WS turn or background task.
func WithModelAttributes(ctx context.Context, service *ModelAttributeService) context.Context {
	return context.WithValue(ctx, modelAttributeContextKey{}, service.NewSession())
}

func ModelAttributesFromContext(ctx context.Context) *ModelAttributeSession {
	value, _ := ctx.Value(modelAttributeContextKey{}).(*ModelAttributeSession)
	return value
}

func RenewModelAttributes(ctx context.Context) context.Context {
	if session := ModelAttributesFromContext(ctx); session != nil {
		return WithModelAttributes(ctx, session.service)
	}
	return ctx
}

func CopyModelAttributes(target, source context.Context) context.Context {
	if session := ModelAttributesFromContext(source); session != nil {
		return context.WithValue(target, modelAttributeContextKey{}, session)
	}
	return target
}

func (s *ModelAttributeSession) Resolve(ctx context.Context, groupID int64, target modelcatalog.Target) (modelcatalog.Resolution, error) {
	if s == nil || s.service == nil {
		return (modelcatalog.View{}).Resolve(target, modelcatalog.Attributes{}), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	group, found := s.groups[groupID]
	if !found {
		group.resolutions = make(map[modelcatalog.TargetKey]modelcatalog.Resolution)
		if groupID > 0 && s.service.Repo != nil {
			group.config, group.err = s.service.Repo.ForGroup(ctx, groupID)
			if group.err == nil && group.config != nil {
				var body []byte
				body, group.err = json.Marshal(group.config)
				if group.err == nil {
					group.config = &ModelAttributeConfig{}
					group.err = json.Unmarshal(body, group.config)
				}
			}
		}
		s.groups[groupID] = group
	}
	if group.err != nil {
		return modelcatalog.Resolution{}, apperror.ServiceUnavailable("MODEL_ATTRIBUTES_UNAVAILABLE", "model attribute configuration is unavailable")
	}
	key := target.Key()
	if result, ok := group.resolutions[key]; ok {
		return result.Clone(), nil
	}
	result := s.service.resolve(s.view, group.config, target, s.service.identityCandidates())
	group.resolutions[key] = result.Clone()
	return result, nil
}

func (s *ModelAttributeService) resolve(view modelcatalog.View, config *ModelAttributeConfig, target modelcatalog.Target, candidates func(string) []string) modelcatalog.Resolution {
	target = target.WithServiceIdentity()
	override := modelcatalog.Attributes{}
	if config != nil && config.Status == StatusActive {
		override = config.match(target.Model, candidates, target)
	}
	result := view.Resolve(target, override)
	// Callers may inject single-model lookup; production uses a pinned catalogue View.
	if s.Catalog.View == nil && s.Catalog.Lookup != nil {
		result.Attributes = modelcatalog.Merge(s.Catalog.Lookup(target.Model), override)
	}
	return result
}

// Preview applies a draft to the group's saved routes, or loads the saved profile when draft is nil.
func (s *ModelAttributeService) Preview(ctx context.Context, groupID int64, model, clientProtocol string, draft *ModelAttributeConfig) ([]modelcatalog.Resolution, error) {
	if s.Catalogue == nil {
		return nil, apperror.ServiceUnavailable("MODEL_ATTRIBUTES_UNAVAILABLE", "model catalogue is unavailable")
	}
	providers, err := s.Catalogue.Read(ctx, &groupID)
	if err != nil {
		return nil, err
	}
	resolved := s.Catalogue.Resolver.ResolveSelectedWithProviders(ctx, &groupID, "", []string{model}, providers)
	config := draft
	if config == nil {
		config, err = s.Repo.ForGroup(ctx, groupID)
		if err != nil {
			return nil, err
		}
	}
	result := []modelcatalog.Resolution{}
	view := s.viewForContext(ctx)
	candidates := s.identityCandidates()
	for _, value := range resolved.Models {
		for _, target := range value.AttributeTargets {
			if clientProtocol == "" || target.ClientProtocol == clientProtocol {
				result = append(result, s.resolve(view, config, target, candidates))
			}
		}
	}
	return result, nil
}

// Unavailable reports whether any group configuration read failed in this session.
func (s *ModelAttributeSession) Unavailable() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, group := range s.groups {
		if group.err != nil {
			return true
		}
	}
	return false
}

func (s *ModelAttributeService) identityCandidates() func(string) []string {
	if s.Catalog.Candidates != nil {
		return s.Catalog.Candidates()
	}
	return nil
}
