package grok

import (
	"strings"
	"sync/atomic"
	"time"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// audio_usage.go recognizes realtime audio events and estimates Grok Voice
// usage in endpoint-specific billing units.
// Voice endpoints meter millions of TTS characters, STT hours, and realtime
// session minutes after callers confirm audio observation. An audio event
// requires a nonempty string payload and an audio
// type that is not a transcript. Relay completion is received before its
// audio flag is read, giving the caller the observation available after one
// relay result.
//
// STT duration can come from the response, elapsed request time, client
// input, or a body-size estimate. A selected duration equal to a positive
// client value is compared with elapsed time and the estimate to correct
// unusually small values. These estimates include request overhead; content
// type does not select a different formula.

// AwaitGrokRealtimeAudioObserved returns one received relay error and the
// audio flag sampled afterward. A nil flag reports false with that error.
// A nil channel blocks, and an exhausted closed channel yields nil error.
//
// RelayRealtime stores audio observations before sending terminal errors,
// so receipt precedes the flag load. The other direction may still progress
// during that sample; this helper neither joins goroutines nor cancels work.
// Callers use the same publication ordering with other producers and retain
// cancellation and connection cleanup responsibility.
func AwaitGrokRealtimeAudioObserved(errCh <-chan error, audioObserved *atomic.Bool) (bool, error) {
	err := <-errCh
	if audioObserved == nil {
		return false, err
	}
	return audioObserved.Load(), err
}

// GrokRealtimeEventHasAudio reports whether a valid JSON event contains audio
// data in audio, delta, or data. The trimmed type must contain audio and
// exclude transcript, and the selected payload must be a nonblank string.
func GrokRealtimeEventHasAudio(msg []byte) bool {
	if !gjson.ValidBytes(msg) {
		return false
	}
	eventType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(msg, "type").String()))
	if !strings.Contains(eventType, "audio") || strings.Contains(eventType, "transcript") {
		return false
	}
	for _, path := range []string{"audio", "delta", "data"} {
		value := gjson.GetBytes(msg, path)
		if value.Type == gjson.String && strings.TrimSpace(value.String()) != "" {
			return true
		}
	}
	return false
}

// EstimateGrokVoiceAudioUsage derives billing units for tts, stt, or
// realtime. Unsupported endpoints and quantities that remain nonpositive
// return nil. For valid JSON, TTS counts the first nonblank trimmed gjson
// string conversion of input, text, or prompt in runes, returning millions of
// characters. It uses raw body bytes when those fields supply no text.
// Realtime uses elapsed minutes; callers establish audio observation before
// billing those minutes.
//
// STT prefers positive response seconds, then elapsed seconds, client
// seconds, and a request-size estimate. When the chosen seconds equal a
// positive client duration below half the larger size or elapsed estimate,
// that larger estimate wins. The result is expressed in hours, and
// contentType does not affect this calculation. Callers supply request timing
// and retain responsibility for when an exchange is billable.
func EstimateGrokVoiceAudioUsage(endpoint string, reqBody []byte, contentType string, respBody []byte, elapsed time.Duration) *protocol.AudioUsage {
	switch strings.TrimSpace(endpoint) {
	case "tts":
		// Read supported JSON text first so Unicode characters count as one unit;
		// raw body length supplies an estimate when the request exposes no usable text.
		chars := 0
		if gjson.ValidBytes(reqBody) {
			for _, key := range []string{"input", "text", "prompt"} {
				if s := strings.TrimSpace(gjson.GetBytes(reqBody, key).String()); s != "" {
					chars = len([]rune(s))
					break
				}
			}
		}
		// The raw-body estimate keeps non-JSON or missing-text inputs measurable;
		// its byte count can differ from the rune count used for recognized JSON text.
		if chars <= 0 {
			chars = len(reqBody)
		}
		if chars <= 0 {
			return nil
		}
		// TTS prices consume millions of character units.
		return &protocol.AudioUsage{Mode: "tts", DurationOrUnits: float64(chars) / 1_000_000.0}
	case "stt":
		// A positive response duration is upstream evidence for STT billing. Other
		// timing sources are considered when the response exposes none of these fields.
		secs := 0.0
		if gjson.ValidBytes(respBody) {
			for _, path := range []string{"duration", "duration_seconds", "audio_duration", "usage.seconds"} {
				if v := gjson.GetBytes(respBody, path); v.Exists() && v.Type == gjson.Number && v.Float() > 0 {
					secs = v.Float()
					break
				}
			}
		}
		// This byte-rate estimate includes multipart framing and JSON overhead,
		// providing a final duration source when response and timing evidence are absent.
		sizeFloor := 0.0
		if len(reqBody) > 0 {
			sizeFloor = float64(len(reqBody)) / 16000.0
		}
		clientSecs := 0.0
		if gjson.ValidBytes(reqBody) {
			if v := gjson.GetBytes(reqBody, "duration_seconds"); v.Exists() && v.Type == gjson.Number {
				clientSecs = v.Float()
			}
		}
		// With no positive response duration, use elapsed time, client input, then
		// the size estimate so missing evidence follows one consistent billing precedence.
		if secs <= 0 {
			secs = elapsed.Seconds()
		}
		if secs <= 0 {
			secs = clientSecs
		}
		if secs <= 0 {
			secs = sizeFloor
		}
		// Equality with the client value activates this floor even if another source
		// selected the same number. Comparing against elapsed time and body size limits
		// unusually small client durations under this estimate.
		if clientSecs > 0 && secs == clientSecs {
			floor := sizeFloor
			if elapsed.Seconds() > floor {
				floor = elapsed.Seconds()
			}
			if floor > 0 && clientSecs < floor*0.5 {
				secs = floor
			}
		}
		if secs <= 0 {
			return nil
		}
		// STT prices consume hours of selected audio duration.
		return &protocol.AudioUsage{Mode: "stt", DurationOrUnits: secs / 3600.0}
	case "realtime":
		// Realtime prices consume minutes of the observed audio session duration.
		mins := elapsed.Minutes()
		if mins <= 0 {
			return nil
		}
		return &protocol.AudioUsage{Mode: "realtime", DurationOrUnits: mins}
	default:
		return nil
	}
}
