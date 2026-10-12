package provider

const (
	// BackgroundSkipAsSkipped counts an unperformed refresh as skipped by default.
	BackgroundSkipAsSkipped BackgroundSkipAction = iota
	// BackgroundSkipAsSuccess counts an unperformed refresh as successful.
	BackgroundSkipAsSuccess
)

// BackgroundSkipAction controls statistics when no refresh exchange occurs.
type BackgroundSkipAction int

// BackgroundRefreshPolicy controls how background refresh counts skipped operations.
type BackgroundRefreshPolicy struct {
	OnLockHeld       BackgroundSkipAction
	OnAlreadyRefresh BackgroundSkipAction
}

func DefaultBackgroundRefreshPolicy() BackgroundRefreshPolicy {
	return BackgroundRefreshPolicy{
		OnLockHeld:       BackgroundSkipAsSkipped,
		OnAlreadyRefresh: BackgroundSkipAsSkipped,
	}
}

func (p BackgroundRefreshPolicy) HandleLockHeld() error {
	if p.OnLockHeld == BackgroundSkipAsSuccess {
		return nil
	}
	return ErrRefreshSkipped
}

func (p BackgroundRefreshPolicy) HandleAlreadyRefreshed() error {
	if p.OnAlreadyRefresh == BackgroundSkipAsSuccess {
		return nil
	}
	return ErrRefreshSkipped
}
