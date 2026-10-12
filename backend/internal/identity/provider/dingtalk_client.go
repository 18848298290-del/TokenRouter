package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// DingTalkClientConfig contains the credentials and endpoints used by DingTalkClient.
type DingTalkClientConfig struct {
	ClientID     string
	ClientSecret string
	TokenURL     string
	UserInfoURL  string
}

type DingTalkClient struct {
	cfg         DingTalkClientConfig
	appToken    string
	appTokenExp time.Time // Provider expiry minus 200 seconds when its lifetime exceeds 200 seconds.
	mu          sync.Mutex
	httpClient  *http.Client
	// TODO(multi-instance): Share the application token cache through Redis.
}

type DingTalkUserTokenResp = identity.DingTalkUserTokenResp

type DingTalkAPIError = identity.DingTalkAPIError

type DingTalkStaffInfo = identity.DingTalkProfileSnapshot

// DingTalkDeptInfo contains department fields returned by topapi/v2/department/get.
type DingTalkDeptInfo struct {
	DeptID   int64
	Name     string
	ParentID int64
}

// DingTalkClients caches a client and its token until the configuration changes.
type DingTalkClients struct {
	mu     sync.Mutex
	client *DingTalkClient
}

func (c *DingTalkClient) ExchangeCodeForUserToken(ctx context.Context, code string) (*DingTalkUserTokenResp, error) {
	body := map[string]string{
		"clientId":     c.cfg.ClientID,
		"clientSecret": c.cfg.ClientSecret,
		"code":         code,
		"grantType":    "authorization_code",
	}
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.TokenURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, ParseDingTalkErr(raw, resp.StatusCode)
	}
	var out DingTalkUserTokenResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return nil, ParseDingTalkErr(raw, resp.StatusCode)
	}
	return &out, nil
}

func ParseDingTalkErr(raw []byte, status int) error {
	var v struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	_ = json.Unmarshal(raw, &v)
	code := v.Code
	if code == "" && v.ErrCode != 0 {
		code = fmt.Sprintf("%d", v.ErrCode)
	}
	msg := v.Message
	if msg == "" {
		msg = v.ErrMsg
	}
	return &DingTalkAPIError{Code: code, Message: msg, HTTP: status}
}

// GetUnionIdByUserToken reads unionId and the user-selected nick from /v1.0/contact/users/me.
// This profile nickname comes from the OIDC endpoint, separate from legacy user/get.nickname.
func (c *DingTalkClient) GetUnionIdByUserToken(ctx context.Context, userToken string) (unionID string, nick string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.UserInfoURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("x-acs-dingtalk-access-token", userToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", ParseDingTalkErr(raw, resp.StatusCode)
	}
	var v struct {
		UnionID string `json:"unionId"`
		Nick    string `json:"nick"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(v.UnionID) == "" {
		return "", "", ParseDingTalkErr(raw, resp.StatusCode)
	}
	return v.UnionID, v.Nick, nil
}

// DingTalkOAPIBase derives the legacy OAPI URL, mapping api.dingtalk.com to oapi.dingtalk.com.
// The getbyunionid and topapi/v2/user/get endpoints use OAPI.
func (c *DingTalkClient) DingTalkOAPIBase() string {
	u, err := url.Parse(c.cfg.UserInfoURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "https://oapi.dingtalk.com"
	}
	host := u.Host
	if after, ok := strings.CutPrefix(host, "api."); ok {
		host = "oapi." + after
	}
	return u.Scheme + "://" + host
}

func (c *DingTalkClient) GetAppToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.appToken != "" && time.Now().Before(c.appTokenExp) {
		return c.appToken, nil
	}
	body := map[string]string{"appKey": c.cfg.ClientID, "appSecret": c.cfg.ClientSecret}
	payload, _ := json.Marshal(body)
	// Request the internal application token from POST /v1.0/oauth2/accessToken.
	// Subsequent legacy OAPI requests use this token in their access_token query parameter.
	appTokenURL := strings.Replace(c.cfg.TokenURL, "/oauth2/userAccessToken", "/oauth2/accessToken", 1)
	if !strings.Contains(appTokenURL, "accessToken") && !strings.Contains(appTokenURL, "gettoken") {
		appTokenURL = c.cfg.TokenURL // fallback for test stub
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, appTokenURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", ParseDingTalkErr(raw, resp.StatusCode)
	}
	var v struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int64  `json:"expireIn"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	if v.AccessToken == "" {
		return "", ParseDingTalkErr(raw, resp.StatusCode)
	}
	c.appToken = v.AccessToken
	ttl := v.ExpireIn
	if ttl > 200 {
		ttl -= 200
	}
	c.appTokenExp = time.Now().Add(time.Duration(ttl) * time.Second)
	return c.appToken, nil
}

func (c *DingTalkClient) GetUserIdByUnionId(ctx context.Context, unionID string) (string, error) {
	appToken, err := c.GetAppToken(ctx)
	if err != nil {
		return "", err
	}
	body := map[string]string{"unionid": unionID}
	payload, _ := json.Marshal(body)
	// Legacy OAPI resolves unionId through POST /topapi/user/getbyunionid.
	// Pass the application token in the access_token query parameter.
	var targetURL string
	if strings.Contains(c.cfg.UserInfoURL, "/contact/users/me") {
		targetURL = c.DingTalkOAPIBase() + "/topapi/user/getbyunionid?access_token=" + url.QueryEscape(appToken)
	} else {
		targetURL = c.cfg.UserInfoURL // fallback for test stub
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", ParseDingTalkErr(raw, resp.StatusCode)
	}
	var v struct {
		Result struct {
			UserID string `json:"userid"`
		} `json:"result"`
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	if v.ErrCode != 0 {
		return "", ParseDingTalkErr(raw, resp.StatusCode)
	}
	if strings.TrimSpace(v.Result.UserID) == "" {
		return "", ParseDingTalkErr(raw, resp.StatusCode)
	}
	return v.Result.UserID, nil
}

// GetDeptInfo fetches one department for hierarchical path construction.
// It calls POST /topapi/v2/department/get with access_token in the query string.
func (c *DingTalkClient) GetDeptInfo(ctx context.Context, deptID int64) (*DingTalkDeptInfo, error) {
	appToken, err := c.GetAppToken(ctx)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"dept_id": deptID, "language": "zh_CN"}
	payload, _ := json.Marshal(body)
	var targetURL string
	if strings.Contains(c.cfg.UserInfoURL, "/contact/users/me") {
		targetURL = c.DingTalkOAPIBase() + "/topapi/v2/department/get?access_token=" + url.QueryEscape(appToken)
	} else {
		targetURL = c.cfg.UserInfoURL // test stub fallback
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, ParseDingTalkErr(raw, resp.StatusCode)
	}
	var v struct {
		Result struct {
			DeptID   int64  `json:"dept_id"`
			Name     string `json:"name"`
			ParentID int64  `json:"parent_id"`
		} `json:"result"`
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	if v.ErrCode != 0 {
		return nil, ParseDingTalkErr(raw, resp.StatusCode)
	}
	return &DingTalkDeptInfo{
		DeptID:   v.Result.DeptID,
		Name:     v.Result.Name,
		ParentID: v.Result.ParentID,
	}, nil
}

func (c *DingTalkClient) GetStaffInfoByUserId(ctx context.Context, userID string) (*DingTalkStaffInfo, error) {
	appToken, err := c.GetAppToken(ctx)
	if err != nil {
		return nil, err
	}
	body := map[string]string{"userid": userID}
	payload, _ := json.Marshal(body)
	// Fetch staff details from the legacy OAPI topapi/v2/user/get endpoint.
	var targetURL string
	if strings.Contains(c.cfg.UserInfoURL, "/contact/users/me") {
		targetURL = c.DingTalkOAPIBase() + "/topapi/v2/user/get?access_token=" + url.QueryEscape(appToken)
	} else {
		targetURL = c.cfg.UserInfoURL // fallback for test stub
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, ParseDingTalkErr(raw, resp.StatusCode)
	}
	var v struct {
		Result struct {
			UserID    string  `json:"userid"`
			Name      string  `json:"name"`
			Nickname  string  `json:"nickname"`
			Email     string  `json:"email"`
			OrgEmail  string  `json:"org_email"`
			Extension string  `json:"extension"`
			DeptID    []int64 `json:"dept_id_list"`
		} `json:"result"`
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	if v.ErrCode != 0 {
		return nil, ParseDingTalkErr(raw, resp.StatusCode)
	}
	if strings.TrimSpace(v.Result.UserID) == "" {
		return nil, ParseDingTalkErr(raw, resp.StatusCode)
	}
	// Email precedence: org_email, email, then the enterprise-email extension field in the JSON-encoded custom fields.
	email := strings.TrimSpace(v.Result.OrgEmail)
	emailSource := "org_email"
	if email == "" {
		email = strings.TrimSpace(v.Result.Email)
		emailSource = "email"
	}
	extensionParsed := false
	if email == "" && strings.TrimSpace(v.Result.Extension) != "" {
		var ext map[string]string
		if err := json.Unmarshal([]byte(v.Result.Extension), &ext); err == nil {
			extensionParsed = true
			if v, ok := ext["企业邮箱"]; ok {
				email = strings.TrimSpace(v)
				emailSource = "extension.企业邮箱"
			}
		}
	}
	if email == "" {
		emailSource = "none"
	}
	slog.Info("dingtalk staff fetched",
		"userid", v.Result.UserID,
		"name_present", v.Result.Name != "",
		"nickname_present", v.Result.Nickname != "",
		"name_eq_nickname", v.Result.Name != "" && v.Result.Name == v.Result.Nickname,
		"email_present", v.Result.Email != "",
		"org_email_present", v.Result.OrgEmail != "",
		"extension_present", v.Result.Extension != "",
		"extension_parsed", extensionParsed,
		"email_source", emailSource,
		"dept_count", len(v.Result.DeptID),
	)
	return &DingTalkStaffInfo{
		UserID:   v.Result.UserID,
		Name:     v.Result.Name,
		Nickname: v.Result.Nickname,
		Email:    email,
		DeptIDs:  v.Result.DeptID,
	}, nil
}

func (s *DingTalkClients) ForConfig(cfg DingTalkClientConfig) *DingTalkClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil || s.client.cfg != cfg {
		s.client = &DingTalkClient{cfg: cfg, httpClient: &http.Client{Timeout: 10 * time.Second}}
	}
	return s.client
}

// ResolveDepartmentPath returns the department hierarchy as a slash-separated path.
func (c *DingTalkClient) ResolveDepartmentPath(ctx context.Context, id int64) (string, error) {
	return ResolveDingTalkDeptPath(ctx, c, id)
}
