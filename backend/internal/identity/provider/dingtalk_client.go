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

// DingTalk login identifies a user by unionID from the user-token endpoint, while
// organization profile reads require an application token and a directory user ID.
// The client resolves those different identifiers before returning staff and
// department data for local profile synchronization. A legacy organization error
// can therefore be interpreted separately from a missing login subject.
//
// Application tokens are cached within one client under a mutex that also covers
// fetching a replacement. Configuration changes select a new client while earlier
// requests can finish on their existing client. Modern and legacy error envelopes
// are normalized into the same identity error representation. Staff email selection
// keeps enterprise email precedence separate from nickname and department data.

// DingTalkClientConfig supplies application credentials and endpoints used by DingTalk requests.
type DingTalkClientConfig struct {
	// ClientID and ClientSecret authorize both user-token exchange and application-
	// token refresh for this client. ForConfig copies the pair so each selected
	// configuration keeps its application-token cache tied to those credentials.
	ClientID     string // registered DingTalk application identifier
	ClientSecret string // application token exchange secret
	// TokenURL serves user-token exchange. GetAppToken rewrites the recognized
	// userAccessToken path for application-token exchange, retaining other configured URLs.
	TokenURL string
	// UserInfoURL serves the current-user profile. Directory methods derive a
	// legacy OAPI origin when this value contains /contact/users/me; other values
	// are used directly as their request endpoint.
	UserInfoURL string
}

// DingTalkClient exchanges user credentials and caches an organization application token.
type DingTalkClient struct {
	// cfg is copied when ForConfig constructs this client. HTTP methods read
	// it outside the token lock, so it remains unchanged during concurrent use.
	cfg DingTalkClientConfig
	// appToken and appTokenExp are read and published together under mu. The
	// lock remains held during a refresh request so other organization reads
	// can reuse the replacement once it becomes available.
	appToken    string     // cached organization application token
	appTokenExp time.Time  // local token reuse cutoff
	mu          sync.Mutex // application token cache lock
	// httpClient is shared by this client's requests. ForConfig installs its
	// timeout once; callers leave the transport configuration stable during use.
	httpClient *http.Client
	// TODO(multi-instance): Share the application token cache through Redis.
}

// DingTalkUserTokenResp aliases the user credential response consumed by identity callbacks.
type DingTalkUserTokenResp = identity.DingTalkUserTokenResp

// DingTalkAPIError aliases provider code, message, and HTTP status for policy handling.
type DingTalkAPIError = identity.DingTalkAPIError

// DingTalkStaffInfo aliases the directory profile fields passed to identity synchronization.
type DingTalkStaffInfo = identity.DingTalkProfileSnapshot

// DingTalkDeptInfo holds directory department identity, display name, and parent relation.
type DingTalkDeptInfo struct {
	DeptID int64 // provider directory department identifier
	// Name is trimmed by ResolveDingTalkDeptPath before becoming a path component.
	// Empty names are omitted, affecting which collected ancestor is later removed.
	Name string // returned directory department name
	// ParentID selects the next request in ResolveDingTalkDeptPath. Nonpositive
	// values and self-parents stop that walk before common highest-name removal.
	ParentID int64 // parent directory department identifier
}

// DingTalkClients caches a client and its token until the configuration changes.
type DingTalkClients struct {
	// mu serializes client selection and replacement. client keeps the current
	// configuration's token cache, while already returned clients can finish requests
	// after this holder selects a replacement.
	mu     sync.Mutex      // current client selection lock
	client *DingTalkClient // client for latest configuration
}

// ExchangeCodeForUserToken posts an authorization code using this client's
// application credentials. It accepts HTTP 200 with a decoded nonempty access
// token; provider failures become DingTalkAPIError for the identity callback to
// classify before profile lookup.
func (c *DingTalkClient) ExchangeCodeForUserToken(ctx context.Context, code string) (*DingTalkUserTokenResp, error) {
	body := map[string]string{
		"clientId":     c.cfg.ClientID,
		"clientSecret": c.cfg.ClientSecret,
		"code":         code,
		"grantType":    "authorization_code",
	}
	// These request maps contain JSON-supported scalar values. Encoding is
	// treated as infallible here before the resulting bytes become the request body.
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
	// Keep any bytes returned when reading fails and discard the read error.
	// Acceptance then depends on status and parsing checks of those available bytes.
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

// ParseDingTalkErr converts modern code/message fields or legacy errcode/errmsg
// fields into a DingTalkAPIError carrying the supplied HTTP status. Modern
// nonempty values take precedence; a nonzero legacy numeric code becomes decimal
// text when the modern code is absent.
//
// Decoding is best effort because callers use this helper after an HTTP or
// provider-result failure has already been observed. A decoding error is discarded
// and any fields populated before it remain available. The result is still an API
// error even when its code or message is empty.
func ParseDingTalkErr(raw []byte, status int) error {
	var v struct {
		Code    string `json:"code"`    // modern provider error code
		Message string `json:"message"` // modern provider error message
		ErrCode int    `json:"errcode"` // legacy provider result code
		ErrMsg  string `json:"errmsg"`  // legacy provider result message
	}
	// Status or result checking already selected an error path. Retain any
	// decoded envelope fields and report that provider failure even if JSON decoding fails.
	_ = json.Unmarshal(raw, &v)
	code := v.Code
	// Modern error text takes precedence; convert a legacy numeric code only
	// when needed so identity policy can compare one normalized code string.
	if code == "" && v.ErrCode != 0 {
		code = fmt.Sprintf("%d", v.ErrCode)
	}
	msg := v.Message
	// Legacy messages complete error envelopes that omit modern message text,
	// giving callback diagnostics the provider-supplied description when it is available.
	if msg == "" {
		msg = v.ErrMsg
	}
	return &DingTalkAPIError{Code: code, Message: msg, HTTP: status}
}

// GetUnionIdByUserToken reads the current-user endpoint with the supplied user
// access token and returns unionId plus nick. It requires HTTP 200, decoded JSON,
// and a unionId nonempty after trimming; returned fields retain their supplied text.
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
	// Keep any bytes returned when reading fails and discard the read error.
	// Acceptance then depends on status and parsing checks of those available bytes.
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", ParseDingTalkErr(raw, resp.StatusCode)
	}
	var v struct {
		UnionID string `json:"unionId"` // stable cross-application user subject
		Nick    string `json:"nick"`    // current-user endpoint profile nickname
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(v.UnionID) == "" {
		return "", "", ParseDingTalkErr(raw, resp.StatusCode)
	}
	return v.UnionID, v.Nick, nil
}

// DingTalkOAPIBase derives a legacy API origin from the configured userinfo URL.
// An api. host prefix becomes oapi.; invalid or incomplete URLs select the default
// DingTalk OAPI origin used by organization lookup requests.
func (c *DingTalkClient) DingTalkOAPIBase() string {
	u, err := url.Parse(c.cfg.UserInfoURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		// Use the production legacy origin when the configured userinfo address
		// cannot yield one, so downstream directory requests receive a complete base URL.
		return "https://oapi.dingtalk.com"
	}
	host := u.Host
	if after, ok := strings.CutPrefix(host, "api."); ok {
		// Directory endpoints use the legacy API host alongside the user-token API,
		// so translate the api. prefix while retaining the configured remaining hostname.
		host = "oapi." + after
	}
	return u.Scheme + "://" + host
}

// GetAppToken returns this client's cached application token before its local
// expiry cutoff, otherwise fetching and publishing a replacement. The client mutex
// covers the cache check and the network request so concurrent organization reads
// reuse one successfully fetched value.
//
// A provider lifetime above 200 seconds is shortened by 200 before computing
// the cutoff; shorter, zero, and negative lifetimes are used directly. Requests
// carry ctx, while mutex waiting itself has no context-select path. The token
// and cutoff are updated together under that lock after decoding succeeds.
func (c *DingTalkClient) GetAppToken(ctx context.Context) (string, error) {
	// Hold the token lock across checking, fetching, and publication so another
	// request cannot publish a competing application-token refresh for this client.
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.appToken != "" && time.Now().Before(c.appTokenExp) {
		return c.appToken, nil
	}
	body := map[string]string{"appKey": c.cfg.ClientID, "appSecret": c.cfg.ClientSecret}
	// These request maps contain JSON-supported scalar values. Encoding is
	// treated as infallible here before the resulting bytes become the request body.
	payload, _ := json.Marshal(body)
	// The current user-token path names a different credential from directory
	// authorization. Rewrite that path for application-token exchange used by OAPI calls.
	appTokenURL := strings.Replace(c.cfg.TokenURL, "/oauth2/userAccessToken", "/oauth2/accessToken", 1)
	if !strings.Contains(appTokenURL, "accessToken") && !strings.Contains(appTokenURL, "gettoken") {
		// Retain an unrecognized configured endpoint so direct clients and local
		// fixtures can supply their own application-token exchange URL shape.
		appTokenURL = c.cfg.TokenURL
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
	// Keep any bytes returned when reading fails and discard the read error.
	// Acceptance then depends on status and parsing checks of those available bytes.
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", ParseDingTalkErr(raw, resp.StatusCode)
	}
	var v struct {
		AccessToken string `json:"accessToken"` // returned organization application token
		ExpireIn    int64  `json:"expireIn"`    // seconds
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	if v.AccessToken == "" {
		return "", ParseDingTalkErr(raw, resp.StatusCode)
	}
	c.appToken = v.AccessToken
	ttl := v.ExpireIn
	// Reuse long-lived tokens until 200 seconds before provider expiry.
	if ttl > 200 {
		ttl -= 200
	}
	// Convert the selected provider lifetime to the local wall-clock cutoff.
	c.appTokenExp = time.Now().Add(time.Duration(ttl) * time.Second)
	return c.appToken, nil
}

// GetUserIdByUnionId resolves the stable unionID to a user identifier in this
// application's organization directory. It obtains an application token first;
// HTTP or provider-result failures, including a missing directory user, return
// DingTalkAPIError for the caller's organization policy.
func (c *DingTalkClient) GetUserIdByUnionId(ctx context.Context, unionID string) (string, error) {
	appToken, err := c.GetAppToken(ctx)
	if err != nil {
		return "", err
	}
	body := map[string]string{"unionid": unionID}
	// These request maps contain JSON-supported scalar values. Encoding is
	// treated as infallible here before the resulting bytes become the request body.
	payload, _ := json.Marshal(body)
	// Resolve a login unionID through the legacy directory using the application
	// token in the query, keeping organization user IDs separate from login subjects.
	var targetURL string
	if strings.Contains(c.cfg.UserInfoURL, "/contact/users/me") {
		targetURL = c.DingTalkOAPIBase() + "/topapi/user/getbyunionid?access_token=" + url.QueryEscape(appToken)
	} else {
		// An unrecognized userinfo path is used directly for directory requests,
		// allowing configured clients and fixtures to supply an endpoint with another shape.
		targetURL = c.cfg.UserInfoURL
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
	// Keep any bytes returned when reading fails and discard the read error.
	// Acceptance then depends on status and parsing checks of those available bytes.
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", ParseDingTalkErr(raw, resp.StatusCode)
	}
	var v struct {
		Result struct { // legacy directory result envelope
			UserID string `json:"userid"` // organization directory user identifier
		} `json:"result"`
		ErrCode int    `json:"errcode"` // legacy provider result code
		ErrMsg  string `json:"errmsg"`  // legacy provider result message
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

// GetDeptInfo reads one directory department using the application token before
// path construction. HTTP and provider errcode failures are returned; an accepted
// result can contain zero IDs or an empty name, which ResolveDingTalkDeptPath
// interprets while walking the hierarchy.
func (c *DingTalkClient) GetDeptInfo(ctx context.Context, deptID int64) (*DingTalkDeptInfo, error) {
	appToken, err := c.GetAppToken(ctx)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"dept_id": deptID, "language": "zh_CN"}
	// These request maps contain JSON-supported scalar values. Encoding is
	// treated as infallible here before the resulting bytes become the request body.
	payload, _ := json.Marshal(body)
	var targetURL string
	if strings.Contains(c.cfg.UserInfoURL, "/contact/users/me") {
		targetURL = c.DingTalkOAPIBase() + "/topapi/v2/department/get?access_token=" + url.QueryEscape(appToken)
	} else {
		// An unrecognized userinfo path is used directly for directory requests,
		// allowing configured clients and fixtures to supply an endpoint with another shape.
		targetURL = c.cfg.UserInfoURL
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
	// Keep any bytes returned when reading fails and discard the read error.
	// Acceptance then depends on status and parsing checks of those available bytes.
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, ParseDingTalkErr(raw, resp.StatusCode)
	}
	var v struct {
		Result struct { // legacy directory result envelope
			DeptID   int64  `json:"dept_id"`   // provider directory department identifier
			Name     string `json:"name"`      // directory display name text
			ParentID int64  `json:"parent_id"` // parent directory department identifier
		} `json:"result"`
		ErrCode int    `json:"errcode"` // legacy provider result code
		ErrMsg  string `json:"errmsg"`  // legacy provider result message
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

// GetStaffInfoByUserId reads directory fields for the supplied organization user
// identifier and returns a profile synchronization snapshot. The application token
// authorizes the legacy request; HTTP errors, nonzero errcode, and a missing returned
// userid stop before the snapshot is prepared.
//
// Enterprise email selection prefers trimmed org_email, then email, then the
// enterprise-email member of the JSON-encoded extension map. Invalid extension
// JSON leaves email empty. Names and department identifiers remain profile data
// for the core, while unionID from the separate current-user request remains the
// login subject used by the HTTP flow.
func (c *DingTalkClient) GetStaffInfoByUserId(ctx context.Context, userID string) (*DingTalkStaffInfo, error) {
	appToken, err := c.GetAppToken(ctx)
	if err != nil {
		return nil, err
	}
	body := map[string]string{"userid": userID}
	// These request maps contain JSON-supported scalar values. Encoding is
	// treated as infallible here before the resulting bytes become the request body.
	payload, _ := json.Marshal(body)
	// A directory user ID selects staff details through the application-token
	// API; this profile request does not replace the unionID used for login identity.
	var targetURL string
	if strings.Contains(c.cfg.UserInfoURL, "/contact/users/me") {
		targetURL = c.DingTalkOAPIBase() + "/topapi/v2/user/get?access_token=" + url.QueryEscape(appToken)
	} else {
		// An unrecognized userinfo path is used directly for directory requests,
		// allowing configured clients and fixtures to supply an endpoint with another shape.
		targetURL = c.cfg.UserInfoURL
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
	// Keep any bytes returned when reading fails and discard the read error.
	// Acceptance then depends on status and parsing checks of those available bytes.
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, ParseDingTalkErr(raw, resp.StatusCode)
	}
	var v struct {
		Result struct { // legacy directory result envelope
			UserID    string  `json:"userid"`       // organization directory user identifier
			Name      string  `json:"name"`         // directory display name text
			Nickname  string  `json:"nickname"`     // legacy directory nickname text
			Email     string  `json:"email"`        // personal directory email text
			OrgEmail  string  `json:"org_email"`    // enterprise directory email text
			Extension string  `json:"extension"`    // JSON-encoded custom profile fields
			DeptID    []int64 `json:"dept_id_list"` // provider directory department identifiers
		} `json:"result"`
		ErrCode int    `json:"errcode"` // legacy provider result code
		ErrMsg  string `json:"errmsg"`  // legacy provider result message
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
	// Enterprise email wins before personal email for the synchronized profile.
	// The extension map is consulted after both fields are empty, and failure leaves
	// the snapshot with an empty email for the caller's completion policy.
	email := strings.TrimSpace(v.Result.OrgEmail)
	emailSource := "org_email"
	if email == "" {
		email = strings.TrimSpace(v.Result.Email)
		emailSource = "email"
	}
	extensionParsed := false
	if email == "" && strings.TrimSpace(v.Result.Extension) != "" {
		var ext map[string]string
		// Treat an unusable extension as missing email rather than rejecting the
		// directory profile; the login flow independently handles required-email completion.
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

// ForConfig returns the client selected for the supplied credentials and two
// endpoints. Selection and replacement happen under the holder mutex, so concurrent
// requests obtain a complete client for that exact configuration.
//
// A changed configuration creates a client with an empty application-token cache
// and a ten-second HTTP budget. Earlier callers can continue using their returned
// client; its configuration and transport remain stable. The holder stores one
// current client, and token caching is local to each client instance.
func (s *DingTalkClients) ForConfig(cfg DingTalkClientConfig) *DingTalkClient {
	// Replace the selected client when credentials or endpoints differ so the
	// next configuration starts with its own application-token cache under this holder lock.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil || s.client.cfg != cfg {
		s.client = &DingTalkClient{cfg: cfg, httpClient: &http.Client{Timeout: 10 * time.Second}}
	}
	return s.client
}

// ResolveDepartmentPath delegates directory traversal for the supplied department
// identifier. A completed or bounded walk omits its highest collected name; lookup
// failures return an error, and the identity sync caller decides whether to update
// the local attribute.
func (c *DingTalkClient) ResolveDepartmentPath(ctx context.Context, id int64) (string, error) {
	return ResolveDingTalkDeptPath(ctx, c, id)
}
