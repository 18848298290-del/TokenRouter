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

// TestResolveDingTalkDeptPath_SingleLevel serves a department and its organization
// root through the directory fixture. The returned path contains the department
// name alone, and traversal performs the expected two lookups.
func TestResolveDingTalkDeptPath_SingleLevel(t *testing.T) {
	callCount := 0
	responses := map[string]string{
		"42": `{"errcode":0,"result":{"dept_id":42,"name":"研发部","parent_id":1}}`,
		"1":  `{"errcode":0,"result":{"dept_id":1,"name":"公司","parent_id":0}}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var req struct {
			DeptID int64 `json:"dept_id"` // requested directory department identifier
		}
		// Request decoding is best effort in this fixture. Any decoded department
		// identifier selects its response, and an unmapped selector produces the rejection envelope.
		_ = json.NewDecoder(r.Body).Decode(&req)
		// Fixture response writes discard their errors. The hierarchy assertion and
		// returned lookup error observe which directory result the adapter actually decoded.
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

// TestResolveDingTalkDeptPath_MultiLevel serves a child, parent department, and
// organization root. Traversal returns the parent and child names in hierarchy
// order separated by a slash after removing the root component.
func TestResolveDingTalkDeptPath_MultiLevel(t *testing.T) {
	// Department 42 belongs to department 10, whose parent is root department 1.
	responses := map[string]string{
		"42": `{"errcode":0,"result":{"dept_id":42,"name":"AI研发","parent_id":10}}`,
		"10": `{"errcode":0,"result":{"dept_id":10,"name":"研发部","parent_id":1}}`,
		"1":  `{"errcode":0,"result":{"dept_id":1,"name":"公司","parent_id":0}}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			DeptID int64 `json:"dept_id"` // requested directory department identifier
		}
		// Request decoding is best effort in this fixture. Any decoded department
		// identifier selects its response, and an unmapped selector produces the rejection envelope.
		_ = json.NewDecoder(r.Body).Decode(&req)
		key := fmt.Sprintf("%d", req.DeptID)
		// Fixture response writes discard their errors. The hierarchy assertion and
		// returned lookup error observe which directory result the adapter actually decoded.
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
