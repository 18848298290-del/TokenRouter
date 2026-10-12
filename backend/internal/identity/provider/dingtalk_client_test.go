package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestDingTalkClient_ExchangeCodeForUserToken_Success checks the POST endpoint for
// a successful user-token response. The adapter returns its access token and
// corporation identifier for the subsequent identity callback steps.
func TestDingTalkClient_ExchangeCodeForUserToken_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "/v1.0/oauth2/userAccessToken", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		// The callback fixture returns user credentials for the subsequent adapter
		// assertions. Write errors are discarded, and those assertions inspect decoded credential fields.
		_, _ = w.Write([]byte(`{"accessToken":"USER_TOKEN_X","expireIn":7200,"refreshToken":"R","corpId":"dingABC"}`))
	}))
	defer server.Close()

	cli := &DingTalkClient{
		cfg: DingTalkClientConfig{
			ClientID: "k", ClientSecret: "s",
			TokenURL: server.URL + "/v1.0/oauth2/userAccessToken",
		},
		httpClient: server.Client(),
	}
	resp, err := cli.ExchangeCodeForUserToken(context.Background(), "AUTH_CODE")
	require.NoError(t, err)
	require.Equal(t, "USER_TOKEN_X", resp.AccessToken)
	require.Equal(t, "dingABC", resp.CorpID)
}

// TestDingTalkClient_GetUnionIdByUserToken_Success requires the user-token header
// while serving a current-user profile. The returned unionID and nickname retain
// the values used by the login flow.
func TestDingTalkClient_GetUnionIdByUserToken_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "USER_TOKEN_X", r.Header.Get("x-acs-dingtalk-access-token"))
		w.Header().Set("Content-Type", "application/json")
		// Send current-user identity fields after checking the submitted token header.
		// Write failures are left to the adapter result examined by the unionID and nickname assertions.
		_, _ = w.Write([]byte(`{"nick":"张三","unionId":"UID_AAA","openId":"OPEN","avatarUrl":"http://x"}`))
	}))
	defer server.Close()

	cli := &DingTalkClient{
		cfg:        DingTalkClientConfig{UserInfoURL: server.URL + "/v1.0/contact/users/me"},
		httpClient: server.Client(),
	}
	unionID, nick, err := cli.GetUnionIdByUserToken(context.Background(), "USER_TOKEN_X")
	require.NoError(t, err)
	require.Equal(t, "UID_AAA", unionID)
	require.Equal(t, "张三", nick)
}

// TestDingTalkClient_GetAppToken_Cached requests an application token twice before
// its local expiry cutoff. Both calls return the same token and the fixture
// receives one request, protecting reuse within one client.
func TestDingTalkClient_GetAppToken_Cached(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		// This fixture supplies application-token data, with any response write error
		// discarded before the client's token or directory result is asserted in the test.
		_, _ = w.Write([]byte(`{"accessToken":"APP_TKN","expireIn":7200}`))
	}))
	defer server.Close()

	cli := &DingTalkClient{
		cfg:        DingTalkClientConfig{ClientID: "k", ClientSecret: "s", TokenURL: server.URL + "/gettoken"},
		httpClient: server.Client(),
	}
	t1, err := cli.GetAppToken(context.Background())
	require.NoError(t, err)
	t2, err := cli.GetAppToken(context.Background())
	require.NoError(t, err)
	require.Equal(t, t1, t2)
	require.Equal(t, 1, callCount, "second call should hit cache")
}

// TestDingTalkClient_GetUserIdByUnionId_60011 supplies a cached application token
// and a directory rejection envelope. The returned identity API error retains
// code 60011 so organization policy can distinguish that failure.
func TestDingTalkClient_GetUserIdByUnionId_60011(t *testing.T) {
	appTokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This fixture supplies application-token data, with any response write error
		// discarded before the client's token or directory result is asserted in the test.
		_, _ = w.Write([]byte(`{"accessToken":"APP_TKN","expireIn":7200}`))
	}))
	defer appTokenServer.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Publish the directory rejection with a successful HTTP status. A write
		// error is discarded, while the test checks whether provider code 60011 reaches
		// identity handling.
		_, _ = w.Write([]byte(`{"errcode":60011,"errmsg":"not in directory"}`))
	}))
	defer server.Close()

	cli := &DingTalkClient{
		cfg:        DingTalkClientConfig{TokenURL: appTokenServer.URL + "/gettoken"},
		httpClient: server.Client(),
	}
	cli.appToken = "APP_TKN"
	cli.appTokenExp = time.Now().Add(time.Hour)
	cli.cfg.UserInfoURL = server.URL + "/v1.0/contact/users/byUnionId"

	_, err := cli.GetUserIdByUnionId(context.Background(), "UID_AAA")
	require.Error(t, err)
	apiErr, ok := err.(*DingTalkAPIError)
	require.True(t, ok)
	require.Equal(t, "60011", apiErr.Code)
}

// TestDingTalkClient_GetDeptInfo_Success supplies a cached application token and
// a successful department envelope. The result retains department ID, name, and
// parent ID for hierarchical path construction.
func TestDingTalkClient_GetDeptInfo_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Send directory fields for the adapter's result mapping. The fixture leaves
		// write failures unasserted and checks the decoded department values after the request completes.
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok","result":{"dept_id":42,"name":"AI数据","parent_id":1}}`))
	}))
	defer server.Close()

	cli := &DingTalkClient{
		cfg: DingTalkClientConfig{
			// An unrecognized path uses the supplied endpoint directly, keeping this
			// department fixture independent from legacy origin and endpoint derivation.
			UserInfoURL: server.URL + "/stub",
		},
		httpClient: server.Client(),
	}
	cli.appToken = "APP_TKN"
	cli.appTokenExp = time.Now().Add(time.Hour)

	info, err := cli.GetDeptInfo(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, int64(42), info.DeptID)
	require.Equal(t, "AI数据", info.Name)
	require.Equal(t, int64(1), info.ParentID)
}

// TestDingTalkClient_GetDeptInfo_ErrCode60003 returns a department-not-found
// envelope after the application token is cached. The adapter returns the identity
// API-error type with code 60003 for lookup failure handling.
func TestDingTalkClient_GetDeptInfo_ErrCode60003(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return the department rejection envelope for API-error classification. Write
		// failures are discarded, and the caller's asserted error code records the
		// observed directory result.
		_, _ = w.Write([]byte(`{"errcode":60003,"errmsg":"dept not found"}`))
	}))
	defer server.Close()

	cli := &DingTalkClient{
		cfg:        DingTalkClientConfig{UserInfoURL: server.URL + "/stub"},
		httpClient: server.Client(),
	}
	cli.appToken = "APP_TKN"
	cli.appTokenExp = time.Now().Add(time.Hour)

	_, err := cli.GetDeptInfo(context.Background(), 999)
	require.Error(t, err)
	apiErr, ok := err.(*DingTalkAPIError)
	require.True(t, ok)
	require.Equal(t, "60003", apiErr.Code)
}
