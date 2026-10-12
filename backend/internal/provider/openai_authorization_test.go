package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol/openai"
)

// TestOpenAIAuthorizationStopWaitsForExchange checks that shutdown cancels and waits for an active exchange.
func TestOpenAIAuthorizationStopWaitsForExchange(t *testing.T) {
	store := NewOpenAISessionStore()
	require.False(t, store.runtimeStarted)
	entered := make(chan struct{})
	service := NewOpenAIAuthorization(store, OpenAIAuthOptions{
		Exchange: func(ctx context.Context, _ string, _ string, _ string, _ string, _ string, _ int64) (*openai.OAuthTokenResponse, error) {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	store.Set("fixture", &OpenAIOAuthSession{State: "state", ClientID: "client", CreatedAt: time.Now()})
	service.Start()
	service.Start()
	result := make(chan error, 1)
	go func() {
		_, err := service.ExchangeCode(context.Background(), &OpenAIExchangeCodeInput{SessionID: "fixture", State: "state"})
		result <- err
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, service.StopContext(ctx))
	require.ErrorIs(t, <-result, context.Canceled)
	require.True(t, store.runtimeStopped)
	require.NoError(t, service.StopContext(ctx))
	_, err := service.ExchangeCode(context.Background(), &OpenAIExchangeCodeInput{SessionID: "fixture", State: "state"})
	require.ErrorIs(t, err, ErrProbeStopped)
	_, exists := store.Get("fixture")
	require.True(t, exists, "交换失败不能消费原会话")
}

// TestOpenAIAuthorizationStopReportsUnfinishedExchange checks timeout reporting and rejection of work after
// stop.
func TestOpenAIAuthorizationStopReportsUnfinishedExchange(t *testing.T) {
	store := NewOpenAISessionStore()
	entered := make(chan struct{})
	release := make(chan struct{})
	service := NewOpenAIAuthorization(store, OpenAIAuthOptions{
		Exchange: func(context.Context, string, string, string, string, string, int64) (*openai.OAuthTokenResponse, error) {
			close(entered)
			<-release
			return nil, errors.New("fixture stopped")
		},
	})
	store.Set("fixture", &OpenAIOAuthSession{State: "state", ClientID: "client", CreatedAt: time.Now()})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = service.ExchangeCode(context.Background(), &OpenAIExchangeCodeInput{SessionID: "fixture", State: "state"})
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := service.StopContext(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.True(t, strings.Contains(err.Error(), "OpenAI authorization"))
	service.Start()
	require.False(t, store.runtimeStarted, "停止超时也不能重开清理或交换")
	close(release)
	<-done
	require.ErrorIs(t, service.StopContext(context.Background()), context.DeadlineExceeded)
	store.Stop()
}

func TestShouldApplyChatGPTAccountInfoPlanType(t *testing.T) {
	require.False(t, ShouldApplyChatGPTAccountInfoPlanType("pro", "self_serve_business_usage_based"))
	require.False(t, ShouldApplyChatGPTAccountInfoPlanType("free", "team"))
	require.False(t, ShouldApplyChatGPTAccountInfoPlanType("", ""))
	require.True(t, ShouldApplyChatGPTAccountInfoPlanType("", "pro"))
}

func TestChatGPTAccountInfoBelongsToTokenProvider(t *testing.T) {
	require.False(t, ChatGPTAccountInfoBelongsToTokenProvider(
		&OpenAITokenInfo{ChatGPTAccountID: "personal-a"},
		&openai.ChatGPTAccountInfo{ProviderID: "workspace-b"},
	))
	require.True(t, ChatGPTAccountInfoBelongsToTokenProvider(
		&OpenAITokenInfo{ChatGPTAccountID: "personal-a"},
		&openai.ChatGPTAccountInfo{ProviderID: "PERSONAL-A"},
	))
	// Missing IDs are treated as matching for compatibility.
	require.True(t, ChatGPTAccountInfoBelongsToTokenProvider(
		&OpenAITokenInfo{},
		&openai.ChatGPTAccountInfo{ProviderID: "workspace-b"},
	))
	require.True(t, ChatGPTAccountInfoBelongsToTokenProvider(
		&OpenAITokenInfo{ChatGPTAccountID: "personal-a"},
		&openai.ChatGPTAccountInfo{},
	))
}
