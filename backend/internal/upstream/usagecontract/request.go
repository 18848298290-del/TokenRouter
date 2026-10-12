package usagecontract

import (
	"context"
	"net/http"

	"github.com/TokenFlux/TokenRouter/internal/upstream/usageview"
)

type Request struct {
	BaseURL                                                            string
	APIKey, WalletToken, WalletUserID, ZhipuOrganization, ZhipuProject string                                      `json:"-"`
	Do                                                                 func(*http.Request) (*http.Response, error) `json:"-"`
	Context                                                            func(context.Context) context.Context       `json:"-"`
	ApplyHeaders                                                       func(http.Header)                           `json:"-"`
	Endpoint                                                           func(string, string) string                 `json:"-"`
}

// Adapter creates a request reader per query; provider orchestration owns caches and health updates.
type Adapter interface {
	Name() string
	Query(context.Context, *Request) (*usageview.UpstreamUsageInfo, error)
}

func (r *Request) String() string   { return "upstream usage request" }
func (r *Request) GoString() string { return r.String() }
