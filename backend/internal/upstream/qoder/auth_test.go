package qoder

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Center fixtures inspect the encoded envelope and supplied request context
// independently of live credentials. PAT and refresh cases assert token
// extraction, required security-token handling, and propagation of cancellation
// to the injected transport, keeping authentication failures visible to the
// provider owner.

// TestExchangePATPostsSignedCenterRequest decodes a PAT request captured by a
// local Center fixture. It checks signature presence, personalToken,
// needRefresh=false, and the security token returned in the identity.
func TestExchangePATPostsSignedCenterRequest(t *testing.T) {
	var capturedHeader http.Header
	var capturedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeader = r.Header.Clone()
		decoded, err := DecodeString(readAllString(t, r))
		if err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if err := json.Unmarshal([]byte(decoded), &capturedBody); err != nil {
			t.Fatalf("parse body: %v", err)
		}
		// Send the fixture response after request assertions. A response-writer
		// error is left to the client operation, whose response or error is checked
		// by this test.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":                 "user-1",
			"name":               "User",
			"userType":           "personal_standard",
			"securityOauthToken": "token-1",
			"refreshToken":       "refresh-1",
		})
	}))
	defer server.Close()

	identity, err := ExchangePATContext(context.Background(), "pat-1", &MachineIdentity{
		MachineID:    "machine-1",
		MachineToken: "machine-token",
		MachineType:  "5",
	}, server.URL, nil)
	if err != nil {
		t.Fatalf("ExchangePAT: %v", err)
	}
	if identity.SecurityOauthToken != "token-1" {
		t.Fatalf("token = %q, want token-1", identity.SecurityOauthToken)
	}
	if capturedHeader.Get("signature") == "" {
		t.Fatal("signature header is empty")
	}
	payload, _ := capturedBody["payload"].(string)
	var inner map[string]any
	if err := json.Unmarshal([]byte(payload), &inner); err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	if inner["personalToken"] != "pat-1" {
		t.Fatalf("personalToken = %v, want pat-1", inner["personalToken"])
	}
	if inner["needRefresh"] != false {
		t.Fatalf("needRefresh = %v, want false", inner["needRefresh"])
	}
}

// TestExchangePATContextUsesProvidedDoerAndContext supplies a transport probe
// and a context value to PAT exchange. It checks the selected endpoint, context
// marker, probe invocation, and returned security token.
func TestExchangePATContextUsesProvidedDoerAndContext(t *testing.T) {
	// contextKey identifies the fixture value used to inspect request-context
	// propagation.
	type contextKey string
	ctx := context.WithValue(context.Background(), contextKey("marker"), "ctx-value")
	called := false
	doer := func(req *http.Request) (*http.Response, error) {
		called = true
		if req.Context().Value(contextKey("marker")) != "ctx-value" {
			t.Fatalf("request context marker = %v, want ctx-value", req.Context().Value(contextKey("marker")))
		}
		if req.URL.String() != "https://center.example/algo/api/v3/user/jobToken?Encode=1" {
			t.Fatalf("request URL = %s", req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
				"id":"user-1",
				"name":"User",
				"userType":"personal_standard",
				"securityOauthToken":"token-1",
				"refreshToken":"refresh-1"
			}`)),
			Request: req,
		}, nil
	}

	identity, err := ExchangePATContext(ctx, "pat-1", &MachineIdentity{
		MachineID:    "machine-1",
		MachineToken: "machine-token",
		MachineType:  "5",
	}, "https://center.example", doer)
	if err != nil {
		t.Fatalf("ExchangePATContext: %v", err)
	}
	if !called {
		t.Fatal("provided doer was not called")
	}
	if identity.SecurityOauthToken != "token-1" {
		t.Fatalf("token = %q, want token-1", identity.SecurityOauthToken)
	}
}

// TestRefreshSessionPostsRefreshPayload captures a refresh request and returns a
// replacement token pair. It checks both old request tokens, needRefresh=true,
// and the new identity tokens.
func TestRefreshSessionPostsRefreshPayload(t *testing.T) {
	var capturedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decoded, err := DecodeString(readAllString(t, r))
		if err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if err := json.Unmarshal([]byte(decoded), &capturedBody); err != nil {
			t.Fatalf("parse body: %v", err)
		}
		// Send the fixture response after request assertions. A response-writer
		// error is left to the client operation, whose response or error is checked
		// by this test.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":                 "user-2",
			"name":               "User 2",
			"userType":           "personal_pro",
			"securityOauthToken": "new-token",
			"refreshToken":       "new-refresh",
		})
	}))
	defer server.Close()

	identity, err := RefreshSessionContext(context.Background(), "old-refresh", "old-token", &MachineIdentity{
		MachineID:    "machine-1",
		MachineToken: "machine-token",
		MachineType:  "5",
	}, server.URL, nil)
	if err != nil {
		t.Fatalf("RefreshSession: %v", err)
	}
	if identity.SecurityOauthToken != "new-token" {
		t.Fatalf("token = %q, want new-token", identity.SecurityOauthToken)
	}
	if identity.RefreshToken != "new-refresh" {
		t.Fatalf("refresh token = %q, want new-refresh", identity.RefreshToken)
	}
	payload, _ := capturedBody["payload"].(string)
	var inner map[string]any
	if err := json.Unmarshal([]byte(payload), &inner); err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	if inner["refreshToken"] != "old-refresh" {
		t.Fatalf("refreshToken = %v, want old-refresh", inner["refreshToken"])
	}
	if inner["securityOauthToken"] != "old-token" {
		t.Fatalf("securityOauthToken = %v, want old-token", inner["securityOauthToken"])
	}
	if inner["needRefresh"] != true {
		t.Fatalf("needRefresh = %v, want true", inner["needRefresh"])
	}
}

// TestRefreshSessionContextRejectsEmptySecurityOauthToken supplies a successful
// response with an empty security token. It requires the missing-token error
// rather than accepting the response as an inference identity.
func TestRefreshSessionContextRejectsEmptySecurityOauthToken(t *testing.T) {
	_, err := RefreshSessionContext(context.Background(), "old-refresh", "old-token", &MachineIdentity{
		MachineID:    "machine-1",
		MachineToken: "machine-token",
		MachineType:  "5",
	}, "https://center.example", func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
				"id":"user-2",
				"name":"User 2",
				"userType":"personal_pro",
				"securityOauthToken":"",
				"refreshToken":"new-refresh"
			}`)),
			Request: req,
		}, nil
	})

	if err == nil {
		t.Fatal("expected empty securityOauthToken error")
	}
	if !strings.Contains(err.Error(), "missing securityOauthToken") {
		t.Fatalf("error = %q, want missing securityOauthToken", err.Error())
	}
}

// TestRefreshSessionContextRejectsEmptyInputSecurityOauthToken supplies
// whitespace as the existing security token before refresh. It checks the
// required-token error and confirms the transport probe remains uncalled.
func TestRefreshSessionContextRejectsEmptyInputSecurityOauthToken(t *testing.T) {
	called := false
	_, err := RefreshSessionContext(context.Background(), "old-refresh", "  ", &MachineIdentity{
		MachineID:    "machine-1",
		MachineToken: "machine-token",
		MachineType:  "5",
	}, "https://center.example", func(req *http.Request) (*http.Response, error) {
		called = true
		return nil, nil
	})

	if err == nil {
		t.Fatal("expected empty input securityOauthToken error")
	}
	if !strings.Contains(err.Error(), "requires securityOauthToken") {
		t.Fatalf("error = %q, want requires securityOauthToken", err.Error())
	}
	if called {
		t.Fatal("doer should not be called for empty input securityOauthToken")
	}
}

// TestRefreshSessionContextPropagatesCanceledContextToDoer starts with an
// already cancelled request context. The probe observes that cancellation and
// the wrapped refresh error remains identifiable as context.Canceled.
func TestRefreshSessionContextPropagatesCanceledContextToDoer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	doer := func(req *http.Request) (*http.Response, error) {
		called = true
		if req.Context().Err() != context.Canceled {
			t.Fatalf("request context err = %v, want context.Canceled", req.Context().Err())
		}
		return nil, req.Context().Err()
	}

	_, err := RefreshSessionContext(ctx, "old-refresh", "old-token", &MachineIdentity{
		MachineID:    "machine-1",
		MachineToken: "machine-token",
		MachineType:  "5",
	}, "https://center.example", doer)

	if !called {
		t.Fatal("provided doer was not called")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RefreshSessionContext error = %v, want context.Canceled", err)
	}
}
