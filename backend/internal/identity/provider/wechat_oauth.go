package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// wechat_oauth.go exchanges WeChat authorization codes and retrieves
// channel-specific user information.
// WeChat returns access credentials and channel-specific identity fields in two
// HTTP responses. Code exchange obtains the token and openid, then userinfo uses
// both values in a query request; the identity flow later chooses the stable
// subject and applies cross-channel requirements.

// These defaults select the production WeChat token and userinfo endpoints
// when application assembly creates the HTTP identity client.
const (
	DefaultWeChatTokenURL    = "https://api.weixin.qq.com/sns/oauth2/access_token"
	DefaultWeChatUserInfoURL = "https://api.weixin.qq.com/sns/userinfo"
)

// WeChatOptions supplies application credentials and endpoints for one resolved login channel.
type WeChatOptions struct {
	// AppID and AppSecret identify the resolved channel used by code exchange.
	// TokenURL and UserInfoURL supply separate requests; the caller validates and
	// selects those addresses before invoking the adapter.
	// AppID: resolved application identifier; AppSecret: application exchange secret.
	// TokenURL: code exchange endpoint; UserInfoURL: profile fetch endpoint.
	AppID, AppSecret, TokenURL, UserInfoURL string
}

type (
	// WechatOAuthTokenResponse aliases the identity credential and channel-ID response.
	WechatOAuthTokenResponse = identity.WeChatOAuthTokenResponse
	// WechatOAuthUserInfoResponse aliases the WeChat profile values used for local identity.
	WechatOAuthUserInfoResponse = identity.WeChatOAuthUserInfoResponse
)

// WeChatClient performs channel code exchange and userinfo reads through configured endpoints.
type WeChatClient struct {
	// TokenURL and UserInfoURL are copied into each FetchIdentity request's options.
	// Assembly supplies production addresses; direct callers can select fixture
	// endpoints before the client starts handling requests.
	// TokenURL: code exchange endpoint; UserInfoURL: profile fetch endpoint.
	TokenURL, UserInfoURL string
}

// FetchWeChatOAuthIdentity exchanges the supplied authorization code before
// fetching userinfo with the resulting token and openid. It returns both responses
// after success; a userinfo failure stops the flow even after token exchange succeeded.
func FetchWeChatOAuthIdentity(ctx context.Context, cfg WeChatOptions, code string) (*WechatOAuthTokenResponse, *WechatOAuthUserInfoResponse, error) {
	tokenResp, err := ExchangeWeChatOAuthCode(ctx, cfg, code)
	if err != nil {
		return nil, nil, err
	}
	userInfo, err := FetchWeChatUserInfo(ctx, cfg.UserInfoURL, tokenResp)
	if err != nil {
		return nil, nil, err
	}
	return tokenResp, userInfo, nil
}

// ExchangeWeChatOAuthCode sends a GET grant request with application credentials
// and the trimmed authorization code. The request has a thirty-second budget;
// non-success status, provider errcode, malformed JSON, or an absent access token
// prevents returning a credential.
func ExchangeWeChatOAuthCode(ctx context.Context, cfg WeChatOptions, code string) (*WechatOAuthTokenResponse, error) {
	endpoint, err := url.Parse(cfg.TokenURL)
	if err != nil {
		return nil, fmt.Errorf("parse wechat access token url: %w", err)
	}

	query := endpoint.Query()
	query.Set("appid", cfg.AppID)
	query.Set("secret", cfg.AppSecret)
	query.Set("code", strings.TrimSpace(code))
	query.Set("grant_type", "authorization_code")
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build wechat access token request: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request wechat access token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read wechat access token response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("wechat access token status=%d", resp.StatusCode)
	}

	var tokenResp WechatOAuthTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("decode wechat access token response: %w", err)
	}
	if tokenResp.ErrCode != 0 {
		return nil, fmt.Errorf("wechat access token error=%d %s", tokenResp.ErrCode, strings.TrimSpace(tokenResp.ErrMsg))
	}
	if strings.TrimSpace(tokenResp.AccessToken) == "" {
		return nil, fmt.Errorf("wechat access token missing access_token")
	}
	return &tokenResp, nil
}

// FetchWeChatUserInfo requests the Chinese-language profile using a nonnil token
// response's access token and openid. It requires successful HTTP status, decoded
// JSON, and zero provider errcode; subject completeness is checked by the identity flow.
func FetchWeChatUserInfo(ctx context.Context, userInfoURL string, tokenResp *WechatOAuthTokenResponse) (*WechatOAuthUserInfoResponse, error) {
	if tokenResp == nil {
		return nil, fmt.Errorf("wechat token response is nil")
	}

	endpoint, err := url.Parse(userInfoURL)
	if err != nil {
		return nil, fmt.Errorf("parse wechat userinfo url: %w", err)
	}
	query := endpoint.Query()
	query.Set("access_token", strings.TrimSpace(tokenResp.AccessToken))
	query.Set("openid", strings.TrimSpace(tokenResp.OpenID))
	query.Set("lang", "zh_CN")
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build wechat userinfo request: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request wechat userinfo: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read wechat userinfo response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("wechat userinfo status=%d", resp.StatusCode)
	}

	var userInfo WechatOAuthUserInfoResponse
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return nil, fmt.Errorf("decode wechat userinfo response: %w", err)
	}
	if userInfo.ErrCode != 0 {
		return nil, fmt.Errorf("wechat userinfo error=%d %s", userInfo.ErrCode, strings.TrimSpace(userInfo.ErrMsg))
	}
	return &userInfo, nil
}

// FetchIdentity resolves request options from the supplied channel credentials
// and this client's configured endpoints. It performs code exchange followed by
// userinfo retrieval, returning both provider responses after the two requests succeed.
func (c WeChatClient) FetchIdentity(ctx context.Context, o identity.WeChatOAuthOptions, code string) (*WechatOAuthTokenResponse, *WechatOAuthUserInfoResponse, error) {
	return FetchWeChatOAuthIdentity(ctx, WeChatOptions{AppID: o.AppID, AppSecret: o.AppSecret, TokenURL: c.TokenURL, UserInfoURL: c.UserInfoURL}, code)
}
