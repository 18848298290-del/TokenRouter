package routing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
	"github.com/TokenFlux/TokenRouter/internal/pkg/locale"
)

var (
	ErrAttributeConfigNotFound = apperror.NotFound("ATTRIBUTE_CONFIG_NOT_FOUND", "attribute configuration not found")
	ErrAttributeConfigConflict = apperror.Conflict("ATTRIBUTE_CONFIG_CONFLICT", "configuration name or group association already exists")
)

// ModelAttributeRule 以最终上游模型匹配展示属性，不参与可请求性计算。
type ModelAttributeRule struct {
	ID         string                  `json:"id"`
	Models     []string                `json:"models"`
	Attributes modelcatalog.Attributes `json:"attributes"`
	Match      *ModelAttributeMatch    `json:"match,omitempty"`
}

// ModelAttributeConfig 是可以被多个分组共享的属性档案。
type ModelAttributeConfig struct {
	ExpectedUpdatedAt time.Time            `json:"-"`
	ID                int64                `json:"id"`
	Name              string               `json:"name"`
	Description       string               `json:"description"`
	Status            string               `json:"status"`
	GroupIDs          []int64              `json:"group_ids"`
	Rules             []ModelAttributeRule `json:"rules"`
	CreatedAt         time.Time            `json:"created_at"`
	UpdatedAt         time.Time            `json:"updated_at"`
}

// ModelAttributeRepository 在同一事务中保存规则与分组关联。
type ModelAttributeRepository interface {
	List(context.Context) ([]ModelAttributeConfig, error)
	Get(context.Context, int64) (*ModelAttributeConfig, error)
	ForGroup(context.Context, int64) (*ModelAttributeConfig, error)
	ForGroups(context.Context, []int64) (map[int64]*ModelAttributeConfig, error)
	Save(context.Context, *ModelAttributeConfig) error
	Delete(context.Context, int64) error
}

// ModelAttributeCatalog 是管理员默认查询的只读端口。
type ModelAttributeCatalog struct {
	Index      func() (modelcatalog.Index, ModelAttributeSnapshot)
	View       func() modelcatalog.View
	Snapshot   func() ModelAttributeSnapshot
	Lookup     func(string) modelcatalog.Attributes
	Update     func() error
	Candidates func() func(string) []string
}

type ModelAttributeSnapshot struct {
	Items       []modelcatalog.Entry `json:"items"`
	Version     string               `json:"version"`
	LastUpdated time.Time            `json:"last_updated"`
	LastError   string               `json:"last_error,omitempty"`
}

// EffectiveModelAttributes 保留多路差异，消费者不得据此过滤模型或协议。
type EffectiveModelAttributes = modelcatalog.Presentation

// ModelAttributeService 不保存跨请求配置缓存，所有实例直接读取提交后的数据库状态。
// @project-doc docs/interfaces/model_catalog_and_marketplace.md#model_attributes
type ModelAttributeService struct {
	Repo        ModelAttributeRepository
	Catalog     ModelAttributeCatalog
	Invalidator GroupAuthInvalidator
	Catalogue   *RequestableCatalogue
}

func (s *ModelAttributeService) Save(ctx context.Context, config *ModelAttributeConfig) error {
	config.Name = strings.TrimSpace(config.Name)
	if config.Name == "" || utf8.RuneCountInString(config.Name) > 100 || config.Status != StatusActive && config.Status != StatusDisabled {
		return apperror.BadRequest("INVALID_ATTRIBUTE_CONFIG", "name and active/disabled status are required")
	}
	seenModels := map[string]bool{}
	for i := range config.Rules {
		rule := &config.Rules[i]
		if rule.Match != nil && *rule.Match == (ModelAttributeMatch{}) {
			rule.Match = nil
		}
		if err := rule.Match.Validate(); err != nil {
			return apperror.BadRequest("INVALID_ATTRIBUTE_MATCH", err.Error())
		}
		if len(rule.Models) == 0 {
			return apperror.BadRequest("INVALID_ATTRIBUTE_RULE", "each rule requires a model")
		}
		for j, name := range rule.Models {
			name = strings.TrimSpace(name)
			matchJSON, _ := json.Marshal(rule.Match)
			key := strings.ToLower(name) + ":" + string(matchJSON)
			if name == "" || strings.Contains(strings.TrimSuffix(name, "*"), "*") || seenModels[key] {
				return apperror.BadRequest("INVALID_ATTRIBUTE_RULE", "model patterns must be nonempty and unique, with an optional trailing wildcard")
			}
			seenModels[key] = true
			rule.Models[j] = name
		}
		if err := rule.Attributes.Validate(); err != nil {
			return apperror.BadRequest("INVALID_MODEL_ATTRIBUTES", err.Error())
		}
	}
	seenGroups := map[int64]bool{}
	for _, id := range config.GroupIDs {
		if id <= 0 || seenGroups[id] {
			return apperror.BadRequest("INVALID_ATTRIBUTE_GROUPS", "group IDs must be positive and unique")
		}
		seenGroups[id] = true
	}
	var oldGroups []int64
	oldRules := map[string]ModelAttributeRule{}
	if config.ID != 0 {
		old, err := s.Repo.Get(ctx, config.ID)
		if err != nil {
			return err
		}
		if !config.UpdatedAt.IsZero() && !config.UpdatedAt.Equal(old.UpdatedAt) {
			return locale.ErrConflict
		}
		config.ExpectedUpdatedAt = old.UpdatedAt
		EnsureModelRuleIDs(old)
		for _, rule := range old.Rules {
			oldRules[rule.ID] = rule
		}
		oldGroups = old.GroupIDs
	}
	EnsureModelRuleIDs(config)
	seenRuleIDs := map[string]bool{}
	for i := range config.Rules {
		rule := &config.Rules[i]
		if seenRuleIDs[rule.ID] {
			return apperror.BadRequest("DUPLICATE_CONTENT_ID", "Content ID is duplicated.")
		}
		seenRuleIDs[rule.ID] = true
		if rule.Attributes.DisplayNameLocalization == nil {
			continue
		}
		prior := oldRules[rule.ID].Attributes
		original := ""
		if prior.DisplayName != nil {
			original = *prior.DisplayName
		}
		current := locale.Original(original)
		if prior.DisplayNameLocalization != nil {
			current = prior.DisplayNameLocalization.Content
		}
		next, err := locale.Prepare(current, *rule.Attributes.DisplayNameLocalization, func(value string) error {
			if len([]rune(value)) > 200 {
				return apperror.BadRequest("MODEL_DISPLAY_NAME_TOO_LONG", "Model display name exceeds 200 characters.")
			}
			return nil
		})
		if err != nil {
			return err
		}
		rule.Attributes.DisplayNameLocalization = &locale.Update[string]{Content: next}
		if next.Source != "" {
			rule.Attributes.DisplayName = &next.Source
		} else {
			rule.Attributes.DisplayName = nil
		}
	}
	if config.Rules == nil {
		config.Rules = []ModelAttributeRule{}
	}
	if config.GroupIDs == nil {
		config.GroupIDs = []int64{}
	}
	if err := s.Repo.Save(ctx, config); err != nil {
		return err
	}
	s.invalidate(ctx, append(oldGroups, config.GroupIDs...))
	return nil
}

func (s *ModelAttributeService) Delete(ctx context.Context, id int64) error {
	old, err := s.Repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.Repo.Delete(ctx, id); err != nil {
		return err
	}
	s.invalidate(ctx, old.GroupIDs)
	return nil
}

func (s *ModelAttributeService) invalidate(ctx context.Context, groups []int64) {
	if s.Invalidator == nil {
		return
	}
	seen := map[int64]bool{}
	for _, id := range groups {
		if !seen[id] {
			s.Invalidator.InvalidateAuthCacheByGroupID(ctx, id)
			seen[id] = true
		}
	}
}

// ResolveModels 每个分组读取一次属性档案，按可请求结果中的最终模型生成展示属性。
func (s *ModelAttributeService) ResolveModels(ctx context.Context, groupID int64, models []RequestableModel) (map[string]EffectiveModelAttributes, error) {
	var config *ModelAttributeConfig
	if s.Repo != nil {
		var err error
		config, err = s.Repo.ForGroup(ctx, groupID)
		if err != nil {
			return nil, err
		}
	}
	return s.resolveModels(s.viewForContext(ctx), config, models, locale.FromContext(ctx)), nil
}

// ResolveGroups 批量读取本次展示所需的属性档案，分组之间共享同一次数据库查询。
func (s *ModelAttributeService) ResolveGroups(ctx context.Context, groups map[int64][]RequestableModel) (map[int64]map[string]EffectiveModelAttributes, error) {
	result := make(map[int64]map[string]EffectiveModelAttributes, len(groups))
	ids := make([]int64, 0, len(groups))
	for id, models := range groups {
		if len(models) > 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return result, nil
	}
	configs := map[int64]*ModelAttributeConfig{}
	if s.Repo != nil {
		var err error
		configs, err = s.Repo.ForGroups(ctx, ids)
		if err != nil {
			return nil, err
		}
	}
	view := s.viewForContext(ctx)
	for _, id := range ids {
		result[id] = s.resolveModels(view, configs[id], groups[id], locale.FromContext(ctx))
	}
	return result, nil
}

// resolveModels 将档案规则叠加到最终上游模型的目录属性上。
func (s *ModelAttributeService) resolveModels(view modelcatalog.View, config *ModelAttributeConfig, models []RequestableModel, language string) map[string]EffectiveModelAttributes {
	candidates := s.identityCandidates()
	result := map[string]EffectiveModelAttributes{}
	resolvedTargets := make(map[modelcatalog.TargetKey]modelcatalog.Attributes)
	for _, model := range models {
		routes := model.AttributeTargets
		if len(routes) == 0 {
			names := model.UpstreamModels
			if len(names) == 0 {
				names = []string{model.ID}
			}
			for _, name := range names {
				routes = append(routes, modelcatalog.Target{Model: name})
			}
		}
		values := []modelcatalog.Attributes{}
		var searchTerms []string
		var resolution *locale.Resolution
		seen := make(map[modelcatalog.TargetKey]bool)
		for _, target := range routes {
			key := target.Key()
			if seen[key] {
				continue
			}
			seen[key] = true
			attributeKey := key.ForAttributes()
			base, found := resolvedTargets[attributeKey]
			if !found {
				base = s.resolve(view, config, target, candidates).Attributes
				resolvedTargets[attributeKey] = base
			}
			if base.DisplayNameLocalization != nil {
				searchTerms = append(searchTerms, locale.SearchTexts(base.DisplayNameLocalization.Content, func(text string) []string { return []string{text} })...)
				value, actual := base.DisplayNameLocalization.Resolve(language)
				resolution = &actual
				if value != "" {
					base.DisplayName = &value
				}
				base.DisplayNameLocalization = nil
			}
			values = append(values, base)
		}
		attrs, different := modelcatalog.Common(values)
		result[model.ID] = EffectiveModelAttributes{Attributes: attrs, RouteDifferences: different, SearchTerms: searchTerms, LocalizationResolution: resolution}
	}
	return result
}

func (c *ModelAttributeConfig) match(model string, expand func(string) []string, targets ...modelcatalog.Target) modelcatalog.Attributes {
	var target modelcatalog.Target
	if len(targets) > 0 {
		target = targets[0]
	}
	names := []string{strings.ToLower(strings.TrimSpace(model))}
	if expand != nil {
		names = append(names, expand(model)...)
	}
	for _, name := range names {
		for _, rule := range c.Rules {
			if !rule.Match.Matches(target) {
				continue
			}
			for _, pattern := range rule.Models {
				if strings.EqualFold(pattern, name) {
					return rule.Attributes
				}
			}
		}
	}
	for _, rule := range c.Rules {
		if !rule.Match.Matches(target) {
			continue
		}
		for _, pattern := range rule.Models {
			if !strings.HasSuffix(pattern, "*") {
				continue
			}
			for _, name := range names {
				if strings.HasPrefix(strings.ToLower(name), strings.ToLower(strings.TrimSuffix(pattern, "*"))) {
					return rule.Attributes
				}
			}
		}
	}
	return modelcatalog.Attributes{}
}

// EnsureModelRuleIDs 给历史规则生成稳定标识，编辑模型匹配项后仍可找到对应译文。
func EnsureModelRuleIDs(config *ModelAttributeConfig) {
	for i := range config.Rules {
		if config.Rules[i].ID != "" {
			continue
		}
		body, _ := json.Marshal(config.Rules[i].Models)
		if match := config.Rules[i].Match; match != nil && *match != (ModelAttributeMatch{}) {
			scope, _ := json.Marshal(match)
			body = append(body, scope...)
		}
		sum := sha256.Sum256(body)
		config.Rules[i].ID = hex.EncodeToString(sum[:8])
	}
}
