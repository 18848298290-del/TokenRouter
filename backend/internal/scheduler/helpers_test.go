package scheduler

import (
	"context"
	"sync/atomic"

	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

// Platform aliases keep scheduler fixture platform identities consistent with
// the shared capability catalog values.
const (
	// Anthropic fixture platform identifier.
	PlatformAnthropic = capability.PlatformAnthropic
	// OpenAI fixture platform identifier.
	PlatformOpenAI = capability.PlatformOpenAI
	// Gemini fixture platform identifier.
	PlatformGemini = capability.PlatformGemini
	// Antigravity fixture platform identifier.
	PlatformAntigravity = capability.PlatformAntigravity
	// Qoder fixture platform identifier.
	PlatformQoder = capability.PlatformQoder
	// Grok fixture platform identifier.
	PlatformGrok = capability.PlatformGrok
)

// runtimeSlotCache simulates provider admission and blocks release for
// shutdown-order tests. Tests configure its fields before passing it to the
// concurrency service, and restrict cache calls to the implemented provider
// operations because the remaining embedded methods are unimplemented.
type runtimeSlotCache struct {
	ConcurrencyCache      // unused cache method delegation
	acquired         bool // configured provider admission result
	// releaseStarted and releaseContinue coordinate a blocked cleanup call.
	// When releaseStarted is configured, ReleaseProviderSlot announces entry
	// and waits for releaseContinue before the runtime can observe completed
	// cleanup.
	releaseStarted  chan struct{} // release entry notification channel
	releaseContinue chan struct{} // release completion permission channel
	releases        atomic.Int64  // provider release invocation count
}

// AcquireProviderSlot returns the fixture's configured provider admission
// result without inspecting request inputs.
func (c *runtimeSlotCache) AcquireProviderSlot(context.Context, int64, int, string) (bool, error) {
	return c.acquired, nil
}

// ReleaseProviderSlot counts cleanup and optionally blocks at the test's
// release barrier. A configured notification channel is closed before waiting
// for releaseContinue, allowing the test to observe cleanup that entered but
// has not returned.
//
// Tests use an idempotent owning lease so the configured notification closes
// once. While blocked, the service's runtime registration still belongs to the
// slot, and the shutdown test checks that Stop cannot complete before the
// fixture is permitted to return. Unconfigured barriers allow immediate
// successful cleanup.
func (c *runtimeSlotCache) ReleaseProviderSlot(context.Context, int64, string) error {
	c.releases.Add(1)
	// Signal cache cleanup entry before blocking its return. The shutdown test
	// releases this barrier only after checking that runtime completion still
	// waits for cleanup.
	if c.releaseStarted != nil {
		close(c.releaseStarted)
		<-c.releaseContinue
	}
	return nil
}
