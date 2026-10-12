package testkit

import (
	"context"
	"io"
	"net/http"
	"strings"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// QoderClient captures requests and supplies response fixtures to the production executor.
type QoderClient struct {
	Request  *http.Request
	Requests []*http.Request
	Body     string
	Bodies   [][]byte
	Err      error
	Headers  map[string]string
}

// QoderFixture holds runtime dependencies; blocking-stream tests can replace Client before execution.
type QoderFixture struct {
	Runtime *gatewayprovider.QoderRuntime
	Tokens  *provideradapter.QoderTokenProvider
	Client  qoder.StreamClient
}

type qoderFixtureClient struct{ fixture *QoderFixture }

func (c *QoderClient) StreamRequestContext(ctx context.Context, _ *qoder.SessionContext, _ string, body []byte, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api1.qoder.sh/test", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	c.Request = req
	c.Requests = append(c.Requests, req)
	c.Bodies = append(c.Bodies, append([]byte(nil), body...))
	c.Headers = headers
	if c.Err != nil {
		return nil, c.Err
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(c.Body))}, nil
}

func (c *QoderClient) StreamRequestContextWithDoer(ctx context.Context, _ *qoder.SessionContext, _ string, body []byte, headers map[string]string, doer qoder.RequestDoer) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api1.qoder.sh/test", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	c.Request = req
	c.Requests = append(c.Requests, req)
	c.Bodies = append(c.Bodies, append([]byte(nil), body...))
	c.Headers = headers
	if c.Err != nil {
		return nil, c.Err
	}
	return doer(req)
}

func (c *QoderClient) BodyAt(index int) []byte {
	if c == nil || index < 0 || index >= len(c.Bodies) {
		return nil
	}
	return append([]byte(nil), c.Bodies[index]...)
}

func (c *QoderClient) BodyCount() int {
	if c == nil {
		return 0
	}
	return len(c.Bodies)
}

func (c qoderFixtureClient) StreamRequestContext(ctx context.Context, session *qoder.SessionContext, path string, body []byte, headers map[string]string) (*http.Response, error) {
	return c.fixture.Client.StreamRequestContext(ctx, session, path, body, headers)
}

// NewQoderFixture builds a Qoder runtime using the supplied test dependencies.
func NewQoderFixture(tokens *provideradapter.QoderTokenProvider, client qoder.StreamClient, conversations *qoder.QoderConversationStore) *QoderFixture {
	f := &QoderFixture{Tokens: tokens, Client: client}
	f.Runtime = gatewayprovider.NewQoderRuntime(gatewayprovider.QoderRuntimeOptions{Tokens: tokens, Client: qoderFixtureClient{fixture: f}, Conversations: conversations})
	return f
}

func NewDefaultQoderFixture() (*provider.Record, *QoderFixture, *QoderClient) {
	value := &provider.Record{ID: 8801, Name: "qoder", Platform: provider.PlatformQoder, Type: provider.ProviderTypeCosy, Credentials: map[string]any{}}
	client := &QoderClient{Body: "data: {\"body\":\"{\\\"choices\\\":[{\\\"delta\\\":{\\\"content\\\":\\\"OK\\\"}}]}\"}\n\n" +
		"data: {\"body\":\"{\\\"usage\\\":{\\\"prompt_tokens\\\":1,\\\"completion_tokens\\\":1,\\\"total_tokens\\\":2}}\"}\n\n" +
		"data: {\"body\":\"[DONE]\"}\n\n"}
	tokens := provideradapter.NewQoderTokenProvider(qoder.SessionBuilder{})
	tokens.Core.Sessions = map[int64]provider.QoderSessionCacheEntry[*qoder.SessionContext]{value.ID: {CredentialsHash: provider.QoderCredentialsHash(value.Credentials), Session: &qoder.SessionContext{Identity: &qoder.AuthIdentity{SecurityOauthToken: "token"}}}}
	return value, NewQoderFixture(tokens, client, qoder.NewQoderConversationStore(qoder.QoderConversationTTL)), client
}
