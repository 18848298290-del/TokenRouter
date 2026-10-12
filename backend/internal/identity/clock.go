package identity

import "time"

// operationClock reads the injected clock on each call, defaulting to time.Now.
type operationClock struct{ read func() time.Time }

func (c operationClock) now() time.Time {
	if c.read != nil {
		return c.read()
	}
	return time.Now()
}

func clockFromOptional(clocks []func() time.Time) operationClock {
	if len(clocks) > 0 {
		return operationClock{read: clocks[0]}
	}
	return operationClock{}
}

func (s *SessionService) now() time.Time {
	if s != nil && s.options.Now != nil {
		return s.options.Now()
	}
	return time.Now()
}

func (s *UserAdmin) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
