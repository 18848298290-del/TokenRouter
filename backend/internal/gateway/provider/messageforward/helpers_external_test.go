package messageforward_test

import (
	"io"

	"github.com/gin-gonic/gin"

	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

const defaultMaxLineSize = 500 * 1024 * 1024

type streamReadCloser struct {
	payload []byte
	sent    bool
	err     error
}

// init enables Gin test mode before tests run.
func init() { gin.SetMode(gin.TestMode) }

func newPartialHealthFixture() *provideradapter.UpstreamHealth {
	return gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{Options: provider.HealthOptions{}})
}

func (r *streamReadCloser) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		n := copy(p, r.payload)
		return n, nil
	}
	if r.err != nil {
		return 0, r.err
	}
	return 0, io.EOF
}

func (r *streamReadCloser) Close() error { return nil }
