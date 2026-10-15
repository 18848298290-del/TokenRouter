package usagecontract

import (
	"context"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

// request.go defines usage-query inputs and the adapter interface that consumes
// them.
// Usage queries carry forwarding credentials and optional wallet or team
// identity hints alongside their transport callbacks. JSON excludes those hints
// and callbacks. Request pointers implement fixed ordinary and Go-style string
// descriptions that omit field values; formatting a Request value uses its
// struct representation.

// Request is a borrowed technical snapshot for one upstream usage query.
// Configure the callbacks before execution and keep the snapshot stable until
// the selected adapter finishes reading it.
type Request struct {
	BaseURL string // configured usage-query root
	// APIKey authenticates ordinary queries; WalletToken and WalletUserID
	// select a New API wallet identity; ZhipuOrganization and ZhipuProject
	// select team queries. Each adapter reads the identities relevant to
	// its fixed format. APIKey: forwarding API credential; WalletToken:
	// wallet access credential; WalletUserID: wallet user identifier;
	// ZhipuOrganization: team organization identifier; ZhipuProject:
	// optional team project identifier.
	APIKey, WalletToken, WalletUserID, ZhipuOrganization, ZhipuProject string `json:"-"`
	// Do sends the query through the caller's transport policy. Adapters
	// require this callback and close each usable response body after
	// successful sending.
	Do func(*http.Request) (*http.Response, error) `json:"-"`
	// Context derives the HTTP request context before sending. usageclient
	// requires this callback; provider construction adds transport and
	// redirect policy through it.
	Context func(context.Context) context.Context `json:"-"`
	// ApplyHeaders adds provider headers before usageclient installs query
	// credentials. Adapters require this callback, including a no-op when
	// no override headers are configured.
	ApplyHeaders func(http.Header) `json:"-"`
	// Endpoint joins forwarding-style paths to BaseURL for adapters using
	// usageclient.Get. Management-root adapters construct their own fixed
	// paths through the shared endpoint helpers.
	Endpoint func(string, string) string `json:"-"`
}

// Adapter names and executes a platform's fixed usage-query format. Provider
// orchestration selects adapters and owns query caching and health updates.
type Adapter interface {
	// Name returns the identifier used by the usage adapter registry.
	Name() string
	// Query reads upstream usage with a stable nonnil request snapshot and
	// a usable context. Callers inspect its error before consuming the
	// normalized result; provider orchestration supplies destination policy
	// and transport callbacks.
	Query(context.Context, *Request) (*usageview.UpstreamUsageInfo, error)
}

// String returns a fixed diagnostic label that omits request field values.
func (r *Request) String() string { return "upstream usage request" }

// GoString returns String's fixed label for Go-syntax diagnostic formatting.
func (r *Request) GoString() string { return r.String() }
