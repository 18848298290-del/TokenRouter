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

// Authorization shutdown needs to distinguish a canceled exchange that returns
// from one whose callback remains blocked. The fixtures gate entry before
// stopping the service, then observe cancellation or a retained timeout and
// reject subsequent registration through the service barrier.
//
// Failed exchanges leave their session proof available, and a timeout does not
// permit restarting session cleanup. The ignored-cancellation fixture is
// released and its caller completion is observed before final checks. Separate
// metadata cases protect nonblank token-plan precedence and account identity
// matching, including missing IDs accepted for compatibility; they exercise
// these comparison helpers rather than authenticating JWT contents.

// TestOpenAIAuthorizationStopWaitsForExchange starts an exchange whose
// callback returns after cancellation. Shutdown waits for that operation,
// stops cleanup, rejects later work and leaves the failed exchange's session
// proof available.
func TestOpenAIAuthorizationStopWaitsForExchange(t *testing.T) {
	store := NewOpenAISessionStore()
	require.False(t, store.runtimeStarted)
	entered := make(chan struct{})
	service := NewOpenAIAuthorization(store, OpenAIAuthOptions{
		Exchange: func(ctx context.Context, _ string, _ string, _ string, _ string, _ string, _ int64) (*openai.OAuthTokenResponse, error) {
			// Signal accepted exchange work before waiting for cancellation, so Stop
			// must cancel this callback and observe its return before session cleanup ends.
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	store.Set("fixture", &OpenAIOAuthSession{State: "state", ClientID: "client", CreatedAt: time.Now()})
	service.Start()
	service.Start()
	// The entry signal establishes registered exchange work before stopping;
	// a buffered result lets its caller finish without waiting for the assertion reader.
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

// TestOpenAIAuthorizationStopReportsUnfinishedExchange holds an exchange
// callback beyond the shutdown deadline. Stop reports unfinished work,
// prevents cleanup restart and retains that timeout after the callback is
// released and its caller finishes.
func TestOpenAIAuthorizationStopReportsUnfinishedExchange(t *testing.T) {
	store := NewOpenAISessionStore()
	entered := make(chan struct{})
	release := make(chan struct{})
	service := NewOpenAIAuthorization(store, OpenAIAuthOptions{
		Exchange: func(context.Context, string, string, string, string, string, int64) (*openai.OAuthTokenResponse, error) {
			// Signal exchange entry but keep the callback blocked on release, making
			// shutdown expiry observable while cancellation alone cannot finish this fixture.
			close(entered)
			<-release
			return nil, errors.New("fixture stopped")
		},
	})
	store.Set("fixture", &OpenAIOAuthSession{State: "state", ClientID: "client", CreatedAt: time.Now()})
	// Keep this callback gated beyond shutdown, then observe caller completion
	// after release so the retained-timeout check is made after the exchange actually returns.
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Discard the exchange result; the done signal establishes callback completion
		// before the test checks whether shutdown retains its earlier deadline failure.
		_, _ = service.ExchangeCode(context.Background(), &OpenAIExchangeCodeInput{SessionID: "fixture", State: "state"})
	}()
	<-entered
	// Use a short stop budget while the callback remains gated; deadline expiry
	// must report unfinished work before the fixture releases its exchange response.
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

// TestShouldApplyChatGPTAccountInfoPlanType compares populated and empty token
// plans with account lookup candidates. Existing plans remain selected, a
// blank candidate cannot fill an empty plan, and a nonblank candidate can.
func TestShouldApplyChatGPTAccountInfoPlanType(t *testing.T) {
	require.False(t, ShouldApplyChatGPTAccountInfoPlanType("pro", "self_serve_business_usage_based"))
	require.False(t, ShouldApplyChatGPTAccountInfoPlanType("free", "team"))
	require.False(t, ShouldApplyChatGPTAccountInfoPlanType("", ""))
	require.True(t, ShouldApplyChatGPTAccountInfoPlanType("", "pro"))
}

// TestChatGPTAccountInfoBelongsToTokenProvider compares different, case-varied
// and missing account identities. Mismatched known IDs reject pairing, while
// equal IDs and either missing identity permit pairing for compatibility.
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
