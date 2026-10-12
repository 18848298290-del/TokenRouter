package text

// SingleCountPorts supplies one selection and count operation. Its adapter owns provider and upstream resources.
type SingleCountPorts interface {
	Select() (bool, error)
	Selected()
	SelectionFailed(error)
	Forward() error
	ForwardFailed(error)
}

// RunSingleCountTokens performs one OpenAI counting attempt with no retry or billing step.
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
