package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/protocol/gemini"
	"github.com/TokenFlux/TokenRouter/internal/protocol/google"
)

// stoppingGeminiClient counts refresh attempts and blocks each until context
// cancellation.
type stoppingGeminiClient struct {
	// entered is initialized before refresh and closes on its first attempt.
	// Later retries share that signal while calls atomically counts each
	// invocation.
	entered chan struct{}
	calls   atomic.Int32 // atomic refresh attempt count
}

// ExchangeCode returns the unused-operation error for this refresh-only
// protocol fixture.
func (c *stoppingGeminiClient) ExchangeCode(context.Context, string, string, string, string, string) (*google.TokenResponse, error) {
	return nil, errors.New("not used")
}

// RefreshToken counts attempts, signals first entry, and waits for
// cancellation. Initialize entered before use; canceled calls return
// immediately but still count each retry invocation made by the authorization
// workflow.
func (c *stoppingGeminiClient) RefreshToken(ctx context.Context, _ string, _ string, _ string) (*google.TokenResponse, error) {
	if c.calls.Add(1) == 1 {
		// Publish first entry once through the atomic count so the test can
		// request shutdown after registered cooperative refresh work has actually
		// begun.
		close(c.entered)
	}
	// Stay active until cancellation so shutdown accounts for this request.
	// Later retries can immediately observe that same canceled context and count
	// another invocation.
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestGeminiAuthorizationStopBoundsInFlightRefresh cancels a registered
// refresh while unconditional retries remain. It checks bounded stop
// reporting, stopped admission, eventual return, four calls, and retention of
// the initial shutdown timeout.
func TestGeminiAuthorizationStopBoundsInFlightRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &stoppingGeminiClient{entered: make(chan struct{})}
		authorization := NewGeminiAuthorization(client, nil, nil, GeminiAuthorizationOptions{})
		require.False(t, authorization.Store.runtimeStarted, "构造不启动后台清理")
		authorization.Start()
		authorization.Start()
		done := make(chan error, 1)
		go func() {
			_, err := authorization.RefreshToken(context.Background(), "code_assist", "fixture-token", "")
			done <- err
		}()
		select {
		case <-client.entered:
		case <-time.After(time.Second):
			t.Fatal("刷新未进入")
		}
		budget, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := authorization.StopContext(budget)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Less(t, time.Since(start), time.Second)
		_, err = authorization.RefreshToken(context.Background(), "code_assist", "fixture-token", "")
		require.ErrorContains(t, err, "stopped")
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("原有有限刷新流程未结束")
		}
		// Fixed retry sleeps outlast the short stop budget. Later completion leaves
		// the retained first timeout visible to repeated shutdown callers.
		require.ErrorIs(t, authorization.StopContext(context.Background()), context.DeadlineExceeded)
		require.EqualValues(t, 4, client.calls.Load(), "本次未改变已进入流程的原重试次数")
	})
}

// TestValidateTierID covers empty, permitted ASCII, exact-length, overlong,
// and forbidden character input. Each case checks whether structural
// validation returns an error for the supplied tier text.
func TestValidateTierID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string // authorization rule case name
		tierID  string // input tier identifier text
		wantErr bool   // expected structural validation failure
	}{
		{name: "空字符串合法", tierID: "", wantErr: false},
		{name: "正常 tier_id", tierID: "google_one_free", wantErr: false},
		{name: "包含斜杠", tierID: "tier/sub", wantErr: false},
		{name: "包含连字符", tierID: "gcp-standard", wantErr: false},
		{name: "纯数字", tierID: "12345", wantErr: false},
		{name: "超长字符串（65个字符）", tierID: strings.Repeat("a", 65), wantErr: true},
		{name: "刚好64个字符", tierID: strings.Repeat("b", 64), wantErr: false},
		{name: "非法字符_空格", tierID: "tier id", wantErr: true},
		{name: "非法字符_中文", tierID: "tier_中文", wantErr: true},
		{name: "非法字符_特殊符号", tierID: "tier@id", wantErr: true},
		{name: "非法字符_感叹号", tierID: "tier!id", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateTierID(tt.tierID)
			if tt.wantErr && err == nil {
				t.Fatalf("期望返回错误，但返回 nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("不期望返回错误，但返回: %v", err)
			}
		})
	}
}

// TestExtractTierIDFromAllowedTiers varies default flags, missing IDs, empty
// arrays, and padded text. It checks the configured preferred ID and LEGACY
// fallback from these allowed-tier examples.
func TestExtractTierIDFromAllowedTiers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string // authorization rule case name
		// input upstream allowed tiers
		allowedTiers []gemini.AllowedTier
		want         string // expected configured rule result
	}{
		{
			name:         "nil 列表返回 LEGACY",
			allowedTiers: nil,
			want:         "LEGACY",
		},
		{
			name:         "空列表返回 LEGACY",
			allowedTiers: []gemini.AllowedTier{},
			want:         "LEGACY",
		},
		{
			name: "有 IsDefault 的 tier",
			allowedTiers: []gemini.AllowedTier{
				{ID: "STANDARD", IsDefault: false},
				{ID: "PRO", IsDefault: true},
				{ID: "ENTERPRISE", IsDefault: false},
			},
			want: "PRO",
		},
		{
			name: "没有 IsDefault 取第一个非空",
			allowedTiers: []gemini.AllowedTier{
				{ID: "STANDARD", IsDefault: false},
				{ID: "ENTERPRISE", IsDefault: false},
			},
			want: "STANDARD",
		},
		{
			name: "IsDefault 的 ID 为空，取第一个非空",
			allowedTiers: []gemini.AllowedTier{
				{ID: "", IsDefault: true},
				{ID: "PRO", IsDefault: false},
			},
			want: "PRO",
		},
		{
			name: "所有 ID 都为空返回 LEGACY",
			allowedTiers: []gemini.AllowedTier{
				{ID: "", IsDefault: false},
				{ID: "   ", IsDefault: false},
			},
			want: "LEGACY",
		},
		{
			name: "ID 带空白会被 trim",
			allowedTiers: []gemini.AllowedTier{
				{ID: "  STANDARD  ", IsDefault: true},
			},
			want: "STANDARD",
		},
		{
			name: "单个 tier 且 IsDefault",
			allowedTiers: []gemini.AllowedTier{
				{ID: "ENTERPRISE", IsDefault: true},
			},
			want: "ENTERPRISE",
		},
		{
			name: "单个 tier 非 IsDefault",
			allowedTiers: []gemini.AllowedTier{
				{ID: "ENTERPRISE", IsDefault: false},
			},
			want: "ENTERPRISE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ExtractTierIDFromAllowedTiers(tt.allowedTiers)
			if got != tt.want {
				t.Fatalf("providercore.ExtractTierIDFromAllowedTiers() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestInferGoogleOneTier covers nonpositive storage and boundaries around
// fifteen GB, two TB, and one hundred TB. It checks exact inferred labels,
// including the exclusive Ultra threshold.
func TestInferGoogleOneTier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string // authorization rule case name
		storageBytes int64  // input Drive storage bytes
		want         string // expected configured rule result
	}{
		// Nonpositive storage has an unknown tier.
		{name: "0 bytes -> unknown", storageBytes: 0, want: GeminiTierGoogleOneUnknown},
		{name: "负数 -> unknown", storageBytes: -1, want: GeminiTierGoogleOneUnknown},

		// Storage above 100 TB selects Ultra.
		{name: "> 100TB -> ultra", storageBytes: int64(StorageTierUnlimited) + 1, want: GeminiTierGoogleAIUltra},
		{name: "200TB -> ultra", storageBytes: 200 * int64(TB), want: GeminiTierGoogleAIUltra},

		// From 2 TB through 100 TB selects Pro.
		{name: "正好 2TB -> pro", storageBytes: int64(StorageTierAIPremium), want: GeminiTierGoogleAIPro},
		{name: "5TB -> pro", storageBytes: 5 * int64(TB), want: GeminiTierGoogleAIPro},
		{name: "100TB 正好 -> pro (不是 > 100TB)", storageBytes: int64(StorageTierUnlimited), want: GeminiTierGoogleAIPro},

		// From 15 GB up to 2 TB selects Free.
		{name: "正好 15GB -> free", storageBytes: int64(StorageTierFree), want: GeminiTierGoogleOneFree},
		{name: "100GB -> free", storageBytes: 100 * int64(GB), want: GeminiTierGoogleOneFree},
		{name: "略低于 2TB -> free", storageBytes: int64(StorageTierAIPremium) - 1, want: GeminiTierGoogleOneFree},

		// Storage below 15 GB has an unknown tier.
		{name: "1GB -> unknown", storageBytes: int64(GB), want: GeminiTierGoogleOneUnknown},
		{name: "略低于 15GB -> unknown", storageBytes: int64(StorageTierFree) - 1, want: GeminiTierGoogleOneUnknown},
		{name: "1 byte -> unknown", storageBytes: 1, want: GeminiTierGoogleOneUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := InferGoogleOneTier(tt.storageBytes, t.Logf)
			if got != tt.want {
				t.Fatalf("providercore.InferGoogleOneTier(%d) = %q, want %q", tt.storageBytes, got, tt.want)
			}
		})
	}
}

// TestIsNonRetryableGeminiOAuthError supplies four permanent OAuth markers and
// other diagnostic text. It checks permanent classification and retryable
// outcomes for network, server, empty, and incomplete marker examples.
func TestIsNonRetryableGeminiOAuthError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string // authorization rule case name
		err  error  // input OAuth failure diagnostic
		want bool   // expected configured rule result
	}{
		{name: "invalid_grant", err: fmt.Errorf("error: invalid_grant"), want: true},
		{name: "invalid_client", err: fmt.Errorf("oauth error: invalid_client"), want: true},
		{name: "unauthorized_client", err: fmt.Errorf("unauthorized_client: mismatch"), want: true},
		{name: "access_denied", err: fmt.Errorf("access_denied by user"), want: true},
		{name: "普通网络错误", err: fmt.Errorf("connection timeout"), want: false},
		{name: "HTTP 500 错误", err: fmt.Errorf("server error 500"), want: false},
		{name: "空错误信息", err: fmt.Errorf(""), want: false},
		{name: "包含 invalid 但不是完整匹配", err: fmt.Errorf("invalid request"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := IsNonRetryableGeminiOAuthError(tt.err)
			if got != tt.want {
				t.Fatalf("providercore.IsNonRetryableGeminiOAuthError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
