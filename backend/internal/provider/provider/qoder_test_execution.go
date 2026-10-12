package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	egressadapter "github.com/TokenFlux/TokenRouter/internal/egress/provider"
	providercore "github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

type qoderProviderTestSessionProvider interface {
	GetSession(ctx context.Context, provider *providercore.Record) (*qoder.SessionContext, error)
}

type qoderProviderTestSessionInvalidator interface {
	Invalidate(providerID int64)
}

type qoderProviderTestOAuthClient interface {
	GetUserInfo(ctx context.Context, token string) (*qoder.UserInfo, error)
}

// QoderProviderTest runs upstream probes using the injected shared session provider.
type QoderProviderTest struct {
	Sessions     qoderProviderTestSessionProvider
	Client       qoder.StreamClient
	UserInfo     qoderProviderTestOAuthClient
	Transport    QoderTransport
	Profiles     *egressadapter.TLSProfiles
	RewriteModel func([]byte, string) []byte
}

type qoderTestClientWithDoer interface {
	StreamRequestContextWithDoer(context.Context, *qoder.SessionContext, string, []byte, map[string]string, qoder.RequestDoer) (*http.Response, error)
}

func (s *QoderProviderTest) Execute(c *TestRun, value *providercore.Record, modelID string, prompt string) error {
	if value.Type != capability.ProviderTypeCosy {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Unsupported provider type: %s", value.Type))
	}

	ctx := c.Context
	testModelID := strings.TrimSpace(modelID)
	if testModelID == "" {
		testModelID = "auto"
	}
	testPrompt := strings.TrimSpace(prompt)
	if testPrompt == "" {
		testPrompt = "hi"
	}

	sessionProvider := s.Sessions
	if sessionProvider == nil {
		sessionProvider = NewQoderTokenProvider(qoder.SessionBuilder{})
	}
	if strings.TrimSpace(value.GetCredential("pat")) != "" {
		if invalidator, ok := sessionProvider.(qoderProviderTestSessionInvalidator); ok {
			invalidator.Invalidate(value.ID)
		}
	}
	session, err := sessionProvider.GetSession(ctx, value)
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Qoder session failed: %s", err.Error()))
	}
	site, err := qoderSiteForRecord(value)
	if err != nil {
		return (TestStreamOutput{}).Error(c, err.Error())
	}

	requestBody, err := json.Marshal(map[string]any{
		"model":      testModelID,
		"messages":   []map[string]string{{"role": "user", "content": testPrompt}},
		"max_tokens": 16,
		"stream":     true,
	})
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to encode Qoder test payload")
	}
	mapping := providercore.ResolveModelMapping(value, ModelDefaults())
	if mapped, matched := providercore.ResolveMappedModel(mapping, strings.TrimSpace(qoder.GjsonString(requestBody, "model"))); matched && mapped != "" {
		requestBody = s.RewriteModel(requestBody, mapped)
	}
	payload, modelKey, err := qoder.BuildQoderPayloadFromChatCompletionsForSite(requestBody, qoder.FirstNonEmptyQoder(value.GetCredential("user_type"), "personal_standard"), site)
	if err != nil {
		return (TestStreamOutput{}).Error(c, fmt.Sprintf("Failed to build Qoder test payload: %s", err.Error()))
	}
	payloadBody, err := json.Marshal(payload)
	if err != nil {
		return (TestStreamOutput{}).Error(c, "Failed to encode Qoder test payload")
	}

	c.Begin(true)

	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "test_start", Model: testModelID})
	if site == qoder.SiteGlobal {
		if err := s.probeQoderUserInfo(ctx, value, session); err != nil {
			return (TestStreamOutput{}).Error(c, err.Error())
		}
	}
	(TestStreamOutput{}).SendEvent(c, providercore.TestEvent{Type: "status", Text: "正在通过 Qoder COSY 测试连接"})

	client, err := qoderTestClient(s.Client, value)
	if err != nil {
		return (TestStreamOutput{}).Error(c, err.Error())
	}
	headers := map[string]string{
		"x-model-key":    modelKey,
		"x-model-source": "system",
	}
	if doer := QoderRequestDoer(value, s.Transport, s.Profiles); doer != nil {
		if doerClient, ok := client.(qoderTestClientWithDoer); ok {
			resp, err := doerClient.StreamRequestContextWithDoer(ctx, session, "", payloadBody, headers, doer)
			if err != nil {
				return (TestStreamOutput{}).Error(c, err.Error())
			}
			return (TestStreamOutput{}).Qoder(c, resp.Body)
		}
	}
	resp, err := client.StreamRequestContext(ctx, session, "", payloadBody, headers)
	if err != nil {
		return (TestStreamOutput{}).Error(c, err.Error())
	}

	return (TestStreamOutput{}).Qoder(c, resp.Body)
}

func (s *QoderProviderTest) probeQoderUserInfo(ctx context.Context, value *providercore.Record, session *qoder.SessionContext) error {
	if session == nil || session.Identity == nil {
		return errors.New("qoder session identity is empty")
	}
	token := strings.TrimSpace(session.Identity.SecurityOauthToken)
	if token == "" {
		token = strings.TrimSpace(value.GetCredential("security_oauth_token"))
	}
	if token == "" {
		return errors.New("qoder security_oauth_token is empty")
	}
	client := s.UserInfo
	var userInfo *qoder.UserInfo
	var err error
	if client != nil {
		userInfo, err = client.GetUserInfo(ctx, token)
	} else {
		userInfo, err = s.getQoderUserInfoForProvider(ctx, value, token)
	}
	if err != nil {
		return fmt.Errorf("qoder userinfo probe failed: %w", err)
	}
	if userInfo != nil {
		if session.Identity.UID == "" && strings.TrimSpace(userInfo.ID) != "" {
			session.Identity.UID = strings.TrimSpace(userInfo.ID)
		}
		if session.Identity.Name == "" && strings.TrimSpace(userInfo.Name) != "" {
			session.Identity.Name = strings.TrimSpace(userInfo.Name)
		}
	}
	return nil
}

func (s *QoderProviderTest) getQoderUserInfoForProvider(ctx context.Context, value *providercore.Record, token string) (*qoder.UserInfo, error) {
	profile, err := qoderTestProfile(value)
	if err != nil {
		return nil, err
	}
	if doer := QoderRequestDoer(value, s.Transport, s.Profiles); doer != nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, profile.OpenAPIBaseURL+qoder.UserInfoPath, nil)
		if err != nil {
			return nil, fmt.Errorf("qoder: create userinfo request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
		req.Header.Set("User-Agent", profile.OpenAPIUserAgent())

		resp, err := doer(req)
		if err != nil {
			return nil, fmt.Errorf("qoder: userinfo request: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return nil, fmt.Errorf("qoder: userinfo failed with status %d: %s", resp.StatusCode, qoder.RedactSensitiveText(string(body)))
		}

		var info qoder.UserInfo
		if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
			return nil, fmt.Errorf("qoder: parse userinfo response: %w", err)
		}
		return &info, nil
	}
	return qoder.NewOAuthClientForProfile(profile, nil).GetUserInfo(ctx, token)
}

func qoderTestProfile(value *providercore.Record) (qoder.Profile, error) {
	site, err := qoderSiteForRecord(value)
	if err != nil {
		return qoder.Profile{}, err
	}
	return qoder.ProfileForSite(site)
}

// qoderTestClient uses injected test clients and rebuilds production clients for the provider's site.
func qoderTestClient(configured qoder.StreamClient, value *providercore.Record) (qoder.StreamClient, error) {
	if configured != nil {
		if _, production := configured.(*qoder.Client); !production {
			return configured, nil
		}
	}
	profile, err := qoderTestProfile(value)
	if err != nil {
		return nil, err
	}
	return qoder.NewClientForProfile(profile), nil
}
