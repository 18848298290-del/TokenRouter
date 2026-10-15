package text

// openai_count_tokens.go runs one provider selection and at most one OpenAI
// compatibility token count. Selection latency is recorded even when no target
// is usable, and output/error handling stays with the adapter rather than
// creating generation completion work.

// SingleCountPorts supplies one auxiliary selection and count with
// adapter-owned resources and output.
type SingleCountPorts interface {
	// Select captures one count target and reports availability or a selection
	// error. The loop still invokes Selected afterward, then forwards only when
	// this result is available with no error.
	Select() (bool, error)
	// Selected records completion of the selection phase, including unavailable or
	// failed selection. It runs before the selection result is checked, so
	// implementations can capture latency without assuming a target exists.
	Selected()
	// SelectionFailed handles failed or unavailable selection, with nil error
	// possible for an empty result. The adapter owns final output and cleanup; the
	// loop performs no forwarding after this callback.
	SelectionFailed(error)
	// Forward performs the captured auxiliary count and owns its upstream/provider
	// resources. Nil error ends the loop successfully; a returned error reaches
	// ForwardFailed with no retry or generation completion step.
	Forward() error
	// ForwardFailed observes the returned auxiliary count error after the
	// adapter-owned operation ends. The loop returns afterward, and the specific
	// client response belongs to forwarding/error adapters rather than this
	// notification.
	ForwardFailed(error)
}

// RunSingleCountTokens performs a single auxiliary selection and count using
// adapter-owned resources. Selected runs even on an unavailable or failed
// selection; success returns directly and count errors reach ForwardFailed
// without retry or completion submission.
func RunSingleCountTokens(p SingleCountPorts) {
	available, err := p.Select()
	p.Selected()
	if err != nil || !available {
		p.SelectionFailed(err)
		return
	}
	if err := p.Forward(); err != nil {
		p.ForwardFailed(err)
	}
}
