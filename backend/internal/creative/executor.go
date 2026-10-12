package creative

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// ExecutionGroup 提供当前分组的模型策略和本次调度所需的协议配置。
type ExecutionGroup struct {
	RoutingPolicy    routing.GroupRoutingPolicy
	AllowsOperation  func(string, string) bool
	ConfigureContext func(context.Context, string, string) context.Context
}
type Selection struct {
	ResolveAttributes func(context.Context, string) (modelcatalog.Resolution, error)
	attributes        *modelcatalog.Resolution
	attributeContext  context.Context
	configureContext  func(context.Context) context.Context
	ProviderID        int64
	Platform          string
	Acquired, Waiting bool
	ResolveModel      func(context.Context, string) string
	Release           func()
	Execute           func(context.Context, CreativeRun, CreativeRunPayload, string) ([]CreativeOutput, error)
	Report            func(string, bool)
}
type Executor struct {
	Attributes           *routing.ModelAttributeService
	Group                func(context.Context, int64) (*ExecutionGroup, error)
	OpenAI, Grok, Gemini func(context.Context, CreativeRun) (*Selection, error)
	Timeout              time.Duration
}

type preparedExecution struct {
	selection *Selection
	model     string
	timeout   time.Duration
}

// @project-doc docs/domains/creative_studio.md#creative_model_policy
func (e *Executor) Prepare(ctx context.Context, run CreativeRun) (*CreativeExecution, error) {
	if e == nil {
		return nil, errors.New("creative executor is not configured")
	}
	if e.Group == nil {
		return nil, errors.New("creative group repository is not configured")
	}
	ctx = routing.WithModelAttributes(ctx, e.Attributes)
	group, err := e.Group(ctx, run.GroupID)
	if err != nil || group == nil {
		return nil, CreativeNonRetryableError("creative group %d policy is unavailable", run.GroupID)
	}
	policy := newGroupModelPolicy(group.RoutingPolicy)
	groupModel, allowed := policy.resolve(run.Model)
	if !allowed {
		return nil, CreativeNonRetryableError("creative model %s is restricted by group %d", run.Model, run.GroupID)
	}
	platforms := []string{PlatformOpenAI, PlatformGemini, PlatformGrok}
	if run.Platform != "" {
		platforms = []string{run.Platform}
	}
	var lastErr error
	for _, platform := range platforms {
		if group.AllowsOperation != nil && !group.AllowsOperation(platform, run.Operation) {
			continue
		}
		selectProvider := map[string]func(context.Context, CreativeRun) (*Selection, error){PlatformOpenAI: e.OpenAI, PlatformGrok: e.Grok, PlatformGemini: e.Gemini}[platform]
		if selectProvider == nil {
			continue
		}
		selectionCtx := ctx
		if group.ConfigureContext != nil {
			selectionCtx = group.ConfigureContext(ctx, platform, run.Operation)
		}
		selection, selectErr := selectProvider(selectionCtx, run)
		if selectErr != nil {
			lastErr = selectErr
			continue
		}
		if selection == nil {
			continue
		}
		if !selection.Acquired {
			if selection.Waiting {
				lastErr = ErrCreativeExecutionPending
			}
			continue
		}
		model := strings.TrimSpace(selection.ResolveModel(selectionCtx, groupModel))
		imageModel := CreativePlatformImageModel(selection.Platform, model)
		if selection.ResolveAttributes != nil {
			resolved, resolveErr := selection.ResolveAttributes(selectionCtx, model)
			if resolveErr != nil {
				if selection.Release != nil {
					selection.Release()
				}
				return nil, resolveErr
			}
			selection.attributes = &resolved
			selection.attributeContext = routing.CopyModelAttributes(context.Background(), selectionCtx)
			if resolved.Attributes.OutputModalities != nil {
				imageModel = slices.Contains(*resolved.Attributes.OutputModalities, "image")
			}
		}
		if group.ConfigureContext != nil {
			selection.configureContext = func(ctx context.Context) context.Context { return group.ConfigureContext(ctx, platform, run.Operation) }
		}
		if selection.Platform != platform || !imageModel || !policy.allowsUpstream(model) {
			if selection.Release != nil {
				selection.Release()
			}
			lastErr = CreativeNonRetryableError("creative upstream model %s is unavailable for group %d", model, run.GroupID)
			continue
		}
		return &CreativeExecution{ProviderID: selection.ProviderID, Platform: selection.Platform, UpstreamModel: model, ReleaseFunc: selection.Release, Target: NewExecutionTarget(selection, model, e.Timeout)}, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, CreativeNonRetryableError("no compatible creative provider available for group %d model %s", run.GroupID, run.Model)
}
func (e *Executor) IsRetryable(err error) bool { return IsRetryableCreativeError(err) }

// NewExecutionTarget 将本次提供商、模型、反馈及预算固化，不再次选取提供商。
func NewExecutionTarget(selection *Selection, model string, timeout time.Duration) ExecutionTarget {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return &preparedExecution{selection: selection, model: model, timeout: timeout}
}

func (t *preparedExecution) Execute(ctx context.Context, run CreativeRun, payload CreativeRunPayload) (*CreativeExecuteResult, error) {
	if t == nil || t.selection == nil {
		return nil, errors.New("creative execution context is not configured")
	}
	model := strings.TrimSpace(t.model)
	if model == "" {
		return nil, errors.New("creative execution upstream model is not configured")
	}
	run.RequestedOutputCount = 1
	if t.selection.attributeContext != nil {
		ctx = routing.CopyModelAttributes(ctx, t.selection.attributeContext)
	}
	if t.selection.configureContext != nil {
		ctx = t.selection.configureContext(ctx)
	}
	if resolved := t.selection.attributes; resolved != nil {
		if media := resolved.Attributes.Media; media != nil {
			imageSize := payload.ImageSize
			if imageSize == "" {
				imageSize = run.ImageSize
			}
			if imageSize != "" && media.ImageSizes != nil && !slices.Contains(media.ImageSizes, imageSize) {
				return nil, CreativeNonRetryableError("image size is not supported by the selected route")
			}
			if media.MaxReferenceImages != nil && len(payload.Sources) > *media.MaxReferenceImages {
				return nil, CreativeNonRetryableError("reference image count exceeds the selected route limit")
			}
			for _, item := range []struct {
				value   string
				allowed []string
			}{{payload.Quality, media.Qualities}, {payload.Background, media.BackgroundOptions}, {payload.AspectRatio, media.AspectRatios}} {
				if item.value != "" && item.allowed != nil && !slices.Contains(item.allowed, item.value) {
					return nil, CreativeNonRetryableError("image parameter is not supported by the selected route")
				}
			}
		}
		if payload.ThinkingLevel != "" {
			if supported := resolved.Attributes.EffortSupport(payload.ThinkingLevel); supported != nil && !*supported {
				return nil, CreativeNonRetryableError("reasoning effort is not supported by the selected route")
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	s := t.selection
	switch s.Platform {
	case PlatformOpenAI, PlatformGrok, PlatformGemini:
	default:
		return nil, CreativeNonRetryableError("creative executor unsupported provider platform %s", s.Platform)
	}
	outputs, err := s.Execute(ctx, run, payload, model)
	if err == nil {
		outputs, err = NormalizeCreativeOutputs(outputs)
	}
	if s.Report != nil {
		s.Report(model, err == nil)
	}
	if err != nil {
		return nil, err
	}
	return &CreativeExecuteResult{Outputs: outputs, ProviderID: s.ProviderID}, nil
}
