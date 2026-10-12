package media

import (
	"context"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
	"github.com/TokenFlux/TokenRouter/internal/provider"
)

// VoiceRequest holds an authenticated and moderated voice request.
type VoiceRequest struct {
	Endpoint, ContentType string
	Body                  []byte
}
type VoiceResult struct {
	RequestID, Model, UpstreamModel string
	Headers                         map[string][]string
	Duration                        time.Duration
	AudioUsage                      *protocol.AudioUsage
}
type VoiceOutcome struct {
	Result    *VoiceResult
	Err       error
	RetryNext bool
}
type VoicePorts interface {
	SelectVoice(context.Context, map[int64]struct{}) (provider.ProviderSnapshot, bool, error)
	AcquireVoice(context.Context, provider.ProviderSnapshot) (func(), bool)
	ForwardVoice(context.Context, provider.ProviderSnapshot, VoiceRequest) VoiceOutcome
	CompleteVoice(context.Context, provider.ProviderSnapshot, VoiceRequest, *VoiceResult)
}

// VoiceFailure lets HTTP report exhausted candidates; the upstream response adapter handles other errors.
type VoiceFailure struct {
	NoProviders bool
	Last        error
}

func RunVoice(ctx context.Context, request VoiceRequest, ports VoicePorts) *VoiceFailure {
	excluded := make(map[int64]struct{})
	var last error
	for range 4 {
		selected, present, err := ports.SelectVoice(ctx, excluded)
		if err != nil || !present {
			return &VoiceFailure{NoProviders: last == nil, Last: last}
		}
		release, acquired := ports.AcquireVoice(ctx, selected)
		if !acquired {
			return nil
		}
		outcome := func() VoiceOutcome { defer release(); return ports.ForwardVoice(ctx, selected, request) }()
		if outcome.Err == nil {
			ports.CompleteVoice(ctx, selected, request, outcome.Result)
			return nil
		}
		if outcome.RetryNext {
			excluded[selected.ID] = struct{}{}
			last = outcome.Err
			continue
		}
		return nil
	}
	if last != nil {
		return &VoiceFailure{Last: last}
	}
	return nil
}
