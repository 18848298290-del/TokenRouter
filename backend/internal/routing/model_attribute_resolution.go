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

// ModelAttributeMatch 限定覆盖规则适用的路线；空字段匹配任意值。
type ModelAttributeMatch struct {
	Platform         string `json:"platform,omitempty"`
	ProviderType     string `json:"provider_type,omitempty"`
	ProviderMode     string `json:"provider_mode,omitempty"`
	Site             string `json:"site,omitempty"`
	ClientProtocol   string `json:"client_protocol,omitempty"`
	UpstreamProtocol string `json:"upstream_protocol,omitempty"`
}

type attributeGroupResult struct {
	config *ModelAttributeConfig
	err    error
}

// ModelAttributeSession 固定请求内的目录和分组配置，多个尝试共享读取结果。
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

// WithModelAttributes 为一次请求安装目录快照。WS 每个 turn 和后台任务各自调用。
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
	group, found := s.groups[groupID]
	if !found {
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
	s.mu.Unlock()
	if group.err != nil {
		return modelcatalog.Resolution{}, apperror.ServiceUnavailable("MODEL_ATTRIBUTES_UNAVAILABLE", "model attribute configuration is unavailable")
	}
	var candidates func(string) []string
	if s.service.Catalog.Candidates != nil {
		candidates = s.service.Catalog.Candidates()
	}
	return s.service.resolve(s.view, group.config, target, candidates), nil
}

func (s *ModelAttributeService) resolve(view modelcatalog.View, config *ModelAttributeConfig, target modelcatalog.Target, candidates func(string) []string) modelcatalog.Resolution {
	target = target.WithServiceIdentity()
	override := modelcatalog.Attributes{}
	if config != nil && config.Status == StatusActive {
		override = config.match(target.Model, candidates, target)
	}
	result := view.Resolve(target, override)
	// 轻量调用方可注入单型号读取器，生产装配使用固定目录 View。
	if s.Catalog.View == nil && s.Catalog.Lookup != nil {
		result.Attributes = modelcatalog.Merge(s.Catalog.Lookup(target.Model), override)
	}
	return result
}

// Preview 按已保存的分组路线展示配置草稿的效果。
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
	for _, value := range resolved.Models {
		for _, target := range value.AttributeTargets {
			if clientProtocol == "" || target.ClientProtocol == clientProtocol {
				result = append(result, s.resolve(view, config, target, nil))
			}
		}
	}
	return result, nil
}

// Unavailable 表示本次请求至少有一个分组的属性配置读取失败。
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
