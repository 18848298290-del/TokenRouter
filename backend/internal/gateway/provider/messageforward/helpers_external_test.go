package messageforward_test

import (
	"io"

	"github.com/gin-gonic/gin"

	gatewaytestkit "github.com/TokenFlux/TokenRouter/internal/gateway/testkit"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	provideradapter "github.com/TokenFlux/TokenRouter/internal/provider/provider"
)

// defaultMaxLineSize supplies the normal Anthropic scanner limit while
// individual fixtures replace it to exercise line-size failures.
const defaultMaxLineSize = 500 * 1024 * 1024

// streamReadCloser supplies one payload read followed by a configured failure
// or EOF.
type streamReadCloser struct {
	// payload is copied once into the first supplied buffer; any suffix
	// beyond that buffer is discarded when sent becomes true.
	payload []byte // first-read fixture body bytes
	sent    bool   // first-read completion marker
	// err is returned on every later read when configured; nil produces EOF
	// after the single fixture payload read.
	err error // later read failure value
}

// init selects Gin test mode before constructing external HTTP fixtures.
func init() { gin.SetMode(gin.TestMode) }

// newPartialHealthFixture creates a health observer with default fixture
// options.
func newPartialHealthFixture() *provideradapter.UpstreamHealth {
	return gatewaytestkit.NewHealthObserver(gatewaytestkit.HealthInput{Options: provider.HealthOptions{}})
}

// Read copies the fitting payload prefix on its first invocation, then returns
// the configured error or EOF. This fixture marks the payload sent once and
// does not retry a truncated suffix.
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

// Close returns success because the fixture has no external resource to
// release.
func (r *streamReadCloser) Close() error { return nil }
