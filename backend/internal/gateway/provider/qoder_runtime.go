package provider

import (
	"context"
	"fmt"
	"strings"
	"sync"

	egressprovider "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/protocol"
	openaiwire "github.com/TokenFlux/TokenRouter/internal/protocol/openai"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
	"github.com/TokenFlux/TokenRouter/internal/upstream"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// QoderRuntimeOptions 包含提供商令牌源、传输和平台会话依赖。
type QoderRuntimeOptions struct {
	Attributes    *routing.ModelAttributeService
	Tokens        *provideradapter.QoderTokenProvider
	Client        qoder.StreamClient
	Transport     provideradapter.QoderTransport
	Profiles      *egressprovider.TLSProfiles
	Health        provideradapter.QoderHealthStore
	Conversations *qoder.QoderConversationStore
}

// QoderRuntime 持有唯一平台执行器和会话状态；提供商选择、资金与全局重试在调用方。
type QoderRuntime struct {
	options       QoderRuntimeOptions
	mu            sync.Mutex
	conversations *qoder.QoderConversationStore
	executor      *qoder.Executor
	enter         func() (func(), error)
}

// NewQoderRuntime 构造不启动后台任务，保留已有会话存储的作用域。
func NewQoderRuntime(options QoderRuntimeOptions) *QoderRuntime {
	conversations := options.Conversations
	if conversations == nil {
		conversations = qoder.NewQoderConversationStore(qoder.QoderConversationTTL)
	}
	return &QoderRuntime{options: options, conversations: conversations}
}

// BindAttemptActivity 在开放入口前绑定应用拥有者，保持首次执行器创建时固化的回调。
func (r *QoderRuntime) BindAttemptActivity(enter func() (func(), error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.enter = enter
}

// Executor 按需创建并复用 Qoder 执行器。
func (r *QoderRuntime) Executor() *qoder.Executor {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.executor == nil {
		r.executor = qoder.NewExecutor(qoder.ExecuteOptions{Conversations: r.conversations, Enter: r.enter})
	}
	return r.executor
}

// PrepareQoderTarget 根据本次提供商记录构造 Qoder 执行目标。
func (r *QoderRuntime) PrepareQoderTarget(metadata qoder.RequestMetadata, value *provider.Record, body []byte, wire protocol.ProtocolID, responseModel string) (upstream.Executor, upstream.AttemptInput) {
	return r.Executor(), upstream.AttemptInput{Protocol: wire, Body: mapQoderRequestModel(value, body), ResponseModel: responseModel, Stream: qoder.GjsonBool(body, "stream"), Target: r.Target(metadata, value)}
}

// Target 绑定会话和客户端读取函数，执行时取得所需资源。
func (r *QoderRuntime) Target(metadata qoder.RequestMetadata, value *provider.Record) *qoder.Target {
	site, err := qoderRuntimeSite(value)
	if err != nil {
		site = qoder.SiteGlobal
	}
	id := int64(0)
	userType := "personal_standard"
	if value != nil {
		id = value.ID
		userType = qoder.FirstNonEmptyQoder(value.GetCredential("user_type"), userType)
	}
	return &qoder.Target{
		ProviderID: id, Site: site, UserType: userType, Metadata: metadata,
		Attributes: func(ctx context.Context, model string, wire protocol.ProtocolID) (modelcatalog.Resolution, error) {
			target := value.ModelAttributeTarget(model, string(wire))
			target.ClientProtocol, target.UpstreamProtocol = string(wire), string(wire)
			session := routing.ModelAttributesFromContext(ctx)
			if session == nil {
				session = r.options.Attributes.NewSession()
			}
			var groupID int64
			if group, ok := requeststate.GroupFromContext(ctx); ok && group != nil {
				groupID = group.ID
			}
			return session.Resolve(ctx, groupID, target)
		},
		Session: func(ctx context.Context) (*qoder.SessionContext, error) {
			return r.options.Tokens.GetSession(ctx, value)
		},
		Client: func() (qoder.StreamClient, error) {
			if r.options.Client != nil {
				if _, production := r.options.Client.(*qoder.Client); !production {
					return r.options.Client, nil
				}
			}
			resolved, err := qoderRuntimeSite(value)
			if err != nil {
				return nil, err
			}
			profile, err := qoder.ProfileForSite(resolved)
			if err != nil {
				return nil, err
			}
			return qoder.NewClientForProfile(profile), nil
		},
		Doer: provideradapter.QoderRequestDoer(value, r.options.Transport, r.options.Profiles),
	}
}

// ObserveQoderFailure 将失败交给提供商健康观测器。
func (r *QoderRuntime) ObserveQoderFailure(ctx context.Context, value *provider.Record, err error) {
	if r == nil || value == nil {
		return
	}
	provideradapter.ObserveQoderUpstreamError(ctx, value.ID, r.options.Health, err)
}

func qoderRuntimeSite(value *provider.Record) (qoder.Site, error) {
	if value == nil {
		return qoder.SiteGlobal, fmt.Errorf("qoder: provider is nil")
	}
	return qoder.ParseSite(value.GetCredential("site"))
}

func mapQoderRequestModel(value *provider.Record, body []byte) []byte {
	if value == nil || !value.IsQoder() || len(body) == 0 {
		return body
	}
	model := strings.TrimSpace(qoder.GjsonString(body, "model"))
	if model == "" {
		return body
	}
	mapped, matched := provider.ResolveMappedModel(provider.ResolveModelMapping(value, provideradapter.ModelDefaults()), model)
	if !matched || mapped == "" || mapped == model {
		return body
	}
	return openaiwire.ReplaceModelInBody(body, mapped)
}
