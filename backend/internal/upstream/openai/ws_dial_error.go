package openai

import (
	"errors"
	"fmt"
	"net/http"
)

var (
	ErrOpenAIWSConnQueueFull            = errors.New("openai ws connection queue full")
	ErrOpenAIWSPreferredConnUnavailable = errors.New("openai ws preferred connection unavailable")
)

// WSDialError retains the handshake status, response, and underlying dial error.
type WSDialError struct {
	StatusCode      int
	ResponseHeaders http.Header
	ResponseBody    []byte
	Err             error
}

func (e *WSDialError) Error() string {
	if e == nil {
		return ""
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("openai ws dial failed: status=%d err=%v", e.StatusCode, e.Err)
	}
	return fmt.Sprintf("openai ws dial failed: %v", e.Err)
}

func (e *WSDialError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
