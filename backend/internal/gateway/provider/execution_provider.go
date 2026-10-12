package provider

import (
	"net/http"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/egress"
	"github.com/TokenFlux/TokenRouter/internal/gateway/requeststate"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing"
)

// ExecutionProvider 保存提供商记录和本次请求的路线。
// Route 用于本次请求，provider.Record 提供提供商规则。
type ExecutionProvider struct {
	Capabilities *modelcatalog.Resolution  `json:"-"`
	Record       provider.Record           `json:"-"`
	Route        requeststate.AttemptRoute `json:"-"`
}

// BindExecutionHeaders 返回绑定提供商的 Header 设置函数，调用时读取最新字段。
func BindExecutionHeaders(value *ExecutionProvider) func(http.Header) {
	return func(headers http.Header) {
		provideradapter.ApplyProviderHeaderOverrides(ExecutionProtocolRecord(value), headers)
	}
}

// BindExecutionHeaderValue 返回绑定提供商的 Header 查询函数，调用时读取字段值。
func BindExecutionHeaderValue(value *ExecutionProvider) func(string) (string, bool) {
	return func(name string) (string, bool) {
		return provideradapter.HeaderOverrideValue(ExecutionProtocolRecord(value), name)
	}
}

// NewExecutionProvider 复制提供商记录并设置时钟函数。
func NewExecutionProvider(value *provider.Record) *ExecutionProvider {
	if value == nil {
		return nil
	}
	out := &ExecutionProvider{}
	provider.CopyRecordInto(&out.Record, value)
	out.Record.Now = time.Now
	out.Record.LoadLocation = time.LoadLocation
	return out
}

// View 返回提供商记录，接收者为 nil 时返回 nil。
func (value *ExecutionProvider) View() *provider.Record {
	if value == nil {
		return nil
	}
	return &value.Record
}

func ExecutionRecord(value *ExecutionProvider) *provider.Record {
	if value == nil {
		return nil
	}
	out := provider.CloneRecord(&value.Record)
	out.Now = time.Now
	out.LoadLocation = time.LoadLocation
	return out
}

// ApplyExecutionRecord 原地更新记录，Record 地址和本次路线保持不变。
func ApplyExecutionRecord(out *ExecutionProvider, value *provider.Record) {
	if out == nil || value == nil {
		return
	}
	provider.CopyRecordInto(&out.Record, value)
	out.Record.Now = time.Now
	out.Record.LoadLocation = time.LoadLocation
}

func ExecutionProviders(values []provider.Record) []ExecutionProvider {
	if values == nil {
		return nil
	}
	out := make([]ExecutionProvider, len(values))
	for i := range values {
		provider.CopyRecordInto(&out[i].Record, &values[i])
		out[i].Record.Now = time.Now
		out[i].Record.LoadLocation = time.LoadLocation
	}
	return out
}

func ExecutionRecords(values []ExecutionProvider) []provider.Record {
	if values == nil {
		return nil
	}
	out := make([]provider.Record, len(values))
	for i := range values {
		provider.CopyRecordInto(&out[i], &values[i].Record)
		out[i].Now = time.Now
		out[i].LoadLocation = time.LoadLocation
	}
	return out
}

func ExecutionRecordPointers(values []*ExecutionProvider) []*provider.Record {
	if values == nil {
		return nil
	}
	out := make([]*provider.Record, len(values))
	for i, value := range values {
		out[i] = ExecutionRecord(value)
	}
	return out
}

func ExecutionModelPolicy(value *ExecutionProvider) ModelPolicy {
	out := ModelPolicy{Record: ExecutionRecord(value)}
	if value != nil {
		out.Route = value.Route
	}
	return out
}

func ExecutionProtocolRecord(value *ExecutionProvider) *provider.Record {
	if value == nil {
		return nil
	}
	v := &value.Record
	return &provider.Record{Platform: v.Platform, Type: v.Type, Credentials: v.Credentials, Extra: v.Extra, ParentProviderID: v.ParentProviderID}
}

func ExecutionProtocolTarget(value *ExecutionProvider) provider.ProtocolTarget {
	out := provider.ProtocolTarget{Record: ExecutionProtocolRecord(value)}
	if value != nil {
		out.Protocol = value.Route.Protocol()
	}
	return out
}

func NormalizeExecutionProtocols(value *ExecutionProvider) error {
	record := ExecutionProtocolRecord(value)
	err := provider.NormalizeProviderProtocols(record)
	if value != nil && record != nil {
		value.Record.Credentials = record.Credentials
		value.Record.Extra = record.Extra
	}
	return err
}

func ExecutionSnapshot(value *ExecutionProvider) provider.ProviderSnapshot {
	if value == nil {
		return provider.ProviderSnapshot{}
	}
	v := ExecutionProtocolRecord(value)
	v.ID = value.Record.ID
	v.Status = value.Record.Status
	v.Schedulable = value.Record.Schedulable
	v.Concurrency = value.Record.Concurrency
	v.Priority = value.Record.Priority
	v.ExpiresAt = value.Record.ExpiresAt
	return (ModelPolicy{Record: v, Route: value.Route}).CandidateSnapshot()
}

func ExecutionCandidatePlan(value *ExecutionProvider) (routing.CandidatePlan, bool) {
	if value == nil {
		return routing.CandidatePlan{}, false
	}
	return value.Route.Candidate()
}

func ExecutionRuntimeConfig(value *ExecutionProvider) *provider.RuntimeConfig {
	v := &value.Record
	return &provider.RuntimeConfig{Extra: v.Extra, Concurrency: v.Concurrency, SessionWindowStart: v.SessionWindowStart, SessionWindowEnd: v.SessionWindowEnd}
}

// ExecutionCompletionRecord 返回完成记录需要的提供商字段。
func ExecutionCompletionRecord(value *ExecutionProvider) *provider.Record {
	if value == nil {
		return nil
	}
	v := &value.Record
	return &provider.Record{ID: v.ID, Name: v.Name, Platform: v.Platform, Type: v.Type, Credentials: v.Credentials, Extra: v.Extra, RateMultiplier: v.RateMultiplier, ParentProviderID: v.ParentProviderID}
}

// ExecutionTLSSelection 提取 TLS 资格与配置 ID，供 egress 选择策略。
func ExecutionTLSSelection(value *ExecutionProvider, routerMatch []egress.TLSFingerprintRouterMatchResult) egress.TLSSelection {
	selection := egress.TLSSelection{}
	if value != nil {
		selection.Enabled = value.View().IsTLSFingerprintEnabled()
		selection.DirectProfileID = value.View().GetTLSFingerprintProfileID()
	}
	if len(routerMatch) > 0 {
		selection.RouterMatched = routerMatch[0].Matched
		selection.RouterID = routerMatch[0].RouterID
		selection.RouterProfileID = routerMatch[0].TLSFingerprintProfileID
	}
	return selection
}
