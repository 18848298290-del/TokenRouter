package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestResolveDingTalkDeptPath_SingleLevel checks that the root organization name is omitted.
func TestResolveDingTalkDeptPath_SingleLevel(t *testing.T) {
	callCount := 0
	responses := map[string]string{
		"42": `{"errcode":0,"result":{"dept_id":42,"name":"研发部","parent_id":1}}`,
		"1":  `{"errcode":0,"result":{"dept_id":1,"name":"公司","parent_id":0}}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var req struct {
			DeptID int64 `json:"dept_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if resp, ok := responses[fmt.Sprintf("%d", req.DeptID)]; ok {
			_, _ = w.Write([]byte(resp))
		} else {
			_, _ = w.Write([]byte(`{"errcode":60003,"errmsg":"not found"}`))
		}
	}))
	defer server.Close()

	cli := &DingTalkClient{
		cfg:        DingTalkClientConfig{UserInfoURL: server.URL + "/stub"},
		httpClient: server.Client(),
	}
	cli.appToken = "tok"
	cli.appTokenExp = time.Now().Add(time.Hour)

	path, err := ResolveDingTalkDeptPath(context.Background(), cli, 42)
	require.NoError(t, err)
	require.Equal(t, "研发部", path)
	require.Equal(t, 2, callCount)
}

// TestResolveDingTalkDeptPath_MultiLevel checks slash-separated nested department names.
func TestResolveDingTalkDeptPath_MultiLevel(t *testing.T) {
	// Department 42 belongs to department 10, whose parent is root department 1.
	responses := map[string]string{
		"42": `{"errcode":0,"result":{"dept_id":42,"name":"AI研发","parent_id":10}}`,
		"10": `{"errcode":0,"result":{"dept_id":10,"name":"研发部","parent_id":1}}`,
		"1":  `{"errcode":0,"result":{"dept_id":1,"name":"公司","parent_id":0}}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Use the requested department ID to select its fixture response.
		var req struct {
			DeptID int64 `json:"dept_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		key := fmt.Sprintf("%d", req.DeptID)
		w.Header().Set("Content-Type", "application/json")
		if resp, ok := responses[key]; ok {
			_, _ = w.Write([]byte(resp))
		} else {
			_, _ = w.Write([]byte(`{"errcode":60003,"errmsg":"not found"}`))
		}
	}))
	defer server.Close()

	cli := &DingTalkClient{
		cfg:        DingTalkClientConfig{UserInfoURL: server.URL + "/stub"},
		httpClient: server.Client(),
	}
	cli.appToken = "tok"
	cli.appTokenExp = time.Now().Add(time.Hour)

	path, err := ResolveDingTalkDeptPath(context.Background(), cli, 42)
	require.NoError(t, err)
	require.Equal(t, "研发部/AI研发", path)
}
