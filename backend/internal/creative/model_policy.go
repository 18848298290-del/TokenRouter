package creative

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// groupModelPolicy 固定一次目录查询或执行尝试的分组策略，价格配置不参与模型准入。
type groupModelPolicy struct {
	view *routing.GroupPolicyView
}

// creativeModelQuery 复用一个分组的提供商及模型规则。
type creativeModelQuery struct {
	attributes *routing.ModelAttributeSession
	group      *GroupView
	policy     groupModelPolicy
	providers  map[string][]CatalogProvider
}

func newGroupModelPolicy(policy routing.GroupRoutingPolicy) groupModelPolicy {
	if !policy.Enabled {
		return groupModelPolicy{}
	}
	return groupModelPolicy{view: &routing.GroupPolicyView{
		GroupRoutingPolicy: policy.Clone(),
	}}
}

// resolve 只执行一次分组映射；上游阶段留给具体提供商解析完成后检查。
func (p groupModelPolicy) resolve(requested string) (string, bool) {
	mapped := p.view.ResolveModel(requested)
	if p.view.RestrictionSource() != routing.BillingModelSourceUpstream {
		model := routing.ModelForRestriction(p.view.RestrictionSource(), requested, mapped)
		if p.view.IsModelRestricted(model) {
			return "", false
		}
	}
	return mapped, true
}

func (p groupModelPolicy) allowsUpstream(model string) bool {
	return p.view.RestrictionSource() != routing.BillingModelSourceUpstream || !p.view.IsModelRestricted(model)
}

// candidates 合并可枚举的请求名称，返回可选模型列表。通配符留给后续匹配使用。
func (p groupModelPolicy) candidates(configured []string, providers []CatalogProvider) []string {
	models := make(map[string]struct{})
	add := func(values ...string) {
		for _, model := range values {
			model = strings.TrimSpace(model)
			if model != "" && !strings.ContainsAny(model, "*?") {
				models[model] = struct{}{}
			}
		}
	}
	add(configured...)
	if p.view != nil {
		add(p.view.AllowedModels...)
		for source, target := range p.view.ModelMapping {
			add(source, target)
		}
	}
	for _, provider := range providers {
		if provider == nil || !provider.IsSchedulable() {
			continue
		}
		add(provider.GetConfiguredRequestModels()...)
		for model := range provider.GetModelMapping() {
			add(model)
		}
	}
	result := make([]string, 0, len(models))
	for model := range models {
		result = append(result, model)
	}
	sort.Strings(result)
	return result
}

// prepareCreativeModels 按已开放的操作读取提供商，每个平台读取一次。
func (s *Public) prepareCreativeModels(ctx context.Context, group *GroupView) (*creativeModelQuery, error) {
	if s.ProviderRepo == nil || group == nil || group.ClaudeCodeOnly {
		return nil, nil
	}
	query := &creativeModelQuery{group: group, policy: newGroupModelPolicy(group.RoutingPolicy), providers: map[string][]CatalogProvider{}}
	query.attributes = s.Attributes.NewSession()
	for _, platform := range []string{PlatformOpenAI, PlatformGemini, PlatformGrok} {
		if len(group.Operations[platform]) == 0 {
			continue
		}
		providers, err := s.ProviderRepo.ListSchedulableByGroupIDAndPlatform(ctx, group.ID, platform)
		if err != nil {
			return nil, err
		}
		query.providers[platform] = providers
	}
	return query, nil
}

func creativeSettingsModels(settings []CreativeModelSetting, groupID int64) []string {
	var models []string
	for _, setting := range settings {
		if setting.GroupID == groupID {
			models = append(models, setting.Model)
		}
	}
	return models
}

func (q *creativeModelQuery) candidates(configured []string) []string {
	if q == nil {
		return nil
	}
	var providers []CatalogProvider
	for _, platform := range []string{PlatformOpenAI, PlatformGemini, PlatformGrok} {
		providers = append(providers, q.providers[platform]...)
	}
	models := append([]string(nil), configured...)
	models = append(models, q.group.ModelsList...)
	return q.policy.candidates(models, providers)
}

func (q *creativeModelQuery) resolveModels(ctx context.Context, models []string) (map[string]creativeModelRoute, error) {
	out := make(map[string]creativeModelRoute)
	for _, model := range models {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if _, exists := out[model]; exists {
			continue
		}
		if route, ok, err := q.resolve(ctx, model, true); err != nil {
			return nil, err
		} else if ok {
			out[model] = route
		}
	}
	return out, nil
}

// resolve 校验具体型号，并按 OpenAI、Gemini、Grok 的顺序选择图片能力。
func (q *creativeModelQuery) resolve(ctx context.Context, model string, display bool) (creativeModelRoute, bool, error) {
	if q == nil || ctx.Err() != nil || strings.TrimSpace(model) == "" || strings.ContainsAny(model, "*?") {
		return creativeModelRoute{}, false, nil
	}
	mapped, allowed := q.policy.resolve(model)
	if !allowed {
		return creativeModelRoute{}, false, nil
	}
	for _, platform := range []string{PlatformOpenAI, PlatformGemini, PlatformGrok} {
		route := creativeModelRoute{Platform: platform, OperationAttributes: map[string]modelcatalog.Attributes{}}
		var values []modelcatalog.Attributes
		perOperation := map[string][]modelcatalog.Attributes{}
		for _, provider := range q.providers[platform] {
			if provider == nil || provider.PlatformID() != platform || !provider.IsSchedulable() || !provider.IsModelSupported(mapped) {
				continue
			}
			finalModel := mappedCatalogModel(provider, mapped)
			if !q.policy.allowsUpstream(finalModel) {
				continue
			}
			for _, operation := range q.group.Operations[platform] {
				if !provider.AllowsProtocol(OperationProtocol(platform, operation), q.group.ProtocolFallbacks) {
					continue
				}
				target := modelcatalog.Target{Platform: platform, Model: finalModel}
				if aware, ok := provider.(interface {
					AttributeTarget(string) modelcatalog.Target
				}); ok {
					target = aware.AttributeTarget(finalModel)
				}
				target.ClientProtocol = string(OperationProtocol(platform, operation))
				target.UpstreamProtocol = target.ClientProtocol
				resolved, err := q.attributes.Resolve(ctx, q.group.ID, target)
				if err != nil {
					if !display {
						return creativeModelRoute{}, false, err
					}
					resolved = modelcatalog.Resolution{}
				}
				modalities := resolved.Attributes.OutputModalities
				if modalities != nil && !slices.Contains(*modalities, "image") || modalities == nil && !CreativePlatformImageModel(platform, finalModel) {
					continue
				}
				if route.Model == "" {
					route.Model = finalModel
				}
				if !slices.Contains(route.Operations, operation) {
					route.Operations = append(route.Operations, operation)
				}
				perOperation[operation] = append(perOperation[operation], resolved.Attributes)
				values = append(values, resolved.Attributes)
			}
		}
		if len(route.Operations) > 0 {
			route.Attributes, _ = modelcatalog.Common(values)
			for operation, attributes := range perOperation {
				route.OperationAttributes[operation], _ = modelcatalog.Common(attributes)
			}
			return route, true, nil
		}
	}
	return creativeModelRoute{}, false, nil
}
