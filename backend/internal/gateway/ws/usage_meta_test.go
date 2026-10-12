package ws

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWSPassthroughUsageMeta_InitFromFirstFrame_MappedModelCandidate(t *testing.T) {
	body := []byte(`{"type":"response.create","model":"sol","reasoning":{"effort":"max"}}`)

	meta := NewUsageMeta("sol", body, RequestUsageDecoder{})
	meta.InitFromFirstFrame(body)

	got := meta.ReasoningEffort.Load()
	require.NotNil(t, got, "reasoning effort should be set")
	require.Equal(t, "max", *got, "mapped model gpt-5.6-sol should preserve max")
}

func TestWSPassthroughUsageMeta_InitFromFirstFrame_NonGPT56RecordsExplicitMax(t *testing.T) {
	body := []byte(`{"type":"response.create","model":"deepseek-v4-flash","reasoning":{"effort":"max"}}`)

	meta := NewUsageMeta("deepseek-v4-flash", body, RequestUsageDecoder{})
	meta.CaptureRequestedReasoningEffort(body)
	meta.InitFromFirstFrame(body)

	got := meta.ReasoningEffort.Load()
	require.NotNil(t, got, "显式 max 应按实际请求记录")
	require.Equal(t, "max", *got)
	requested := meta.RequestedReasoningEffort.Load()
	require.NotNil(t, requested)
	require.Equal(t, "max", *requested, "策略改写前的档位必须单独保留")
}

func TestWSPassthroughUsageMeta_CaptureRequestedReasoningEffortPreservesNone(t *testing.T) {
	body := []byte(`{"type":"response.create","model":"gpt-6-astra","reasoning":{"effort":"none"}}`)

	meta := NewUsageMeta("gpt-6-astra", body, RequestUsageDecoder{})
	meta.CaptureRequestedReasoningEffort(body)

	requested := meta.RequestedReasoningEffort.Load()
	require.NotNil(t, requested)
	require.Equal(t, "none", *requested, "映射前的 none 必须保留为客户端原始档位")
}

func TestWSPassthroughUsageMeta_UpdateFromResponseCreate_MappedModelCandidate(t *testing.T) {
	body := []byte(`{"type":"response.create","model":"sol","reasoning":{"effort":"max"}}`)

	meta := NewUsageMeta("sol", body, RequestUsageDecoder{})
	meta.UpdateFromResponseCreate(body)

	got := meta.ReasoningEffort.Load()
	require.NotNil(t, got)
	require.Equal(t, "max", *got, "mapped model should preserve max on multi-turn update")
}

// TestWSUsageModelConcurrentDirections starts session-model reads and writes from the same barrier.
func TestWSUsageModelConcurrentDirections(t *testing.T) {
	m := NewUsageMeta("first", nil, nil)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for range 10000 {
			m.UpdateSessionRequestModel([]byte(`{"type":"session.update","session":{"model":"next"}}`))
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for range 10000 {
			_ = m.RequestModelForFrame(nil)
		}
	}()
	close(start)
	wg.Wait()
}
