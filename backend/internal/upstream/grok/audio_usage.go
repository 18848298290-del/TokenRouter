package grok

import (
	"strings"
	"sync/atomic"
	"time"

	"github.com/tidwall/gjson"

	"github.com/TokenFlux/TokenRouter/internal/protocol"
)

// AwaitGrokRealtimeAudioObserved waits for one relay result and returns the observed audio flag.
func AwaitGrokRealtimeAudioObserved(errCh <-chan error, audioObserved *atomic.Bool) (bool, error) {
	err := <-errCh
	if audioObserved == nil {
		return false, err
	}
	return audioObserved.Load(), err
}

// GrokRealtimeEventHasAudio reports nonempty audio payloads, excluding transcript events.
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

// EstimateGrokVoiceAudioUsage returns TTS units in millions of characters, STT hours,
// or realtime minutes. Other endpoints, including custom voices, return nil.
func EstimateGrokVoiceAudioUsage(endpoint string, reqBody []byte, contentType string, respBody []byte, elapsed time.Duration) *protocol.AudioUsage {
	switch strings.TrimSpace(endpoint) {
	case "tts":
		// Count runes in JSON input, text, or prompt; otherwise use the raw body byte count.
		chars := 0
		if gjson.ValidBytes(reqBody) {
			for _, key := range []string{"input", "text", "prompt"} {
				if s := strings.TrimSpace(gjson.GetBytes(reqBody, key).String()); s != "" {
					chars = len([]rune(s))
					break
				}
			}
		}
		if chars <= 0 {
			chars = len(reqBody)
		}
		if chars <= 0 {
			return nil
		}
		return &protocol.AudioUsage{Mode: "tts", DurationOrUnits: float64(chars) / 1_000_000.0}
	case "stt":
		// Prefer response duration, then elapsed time, client duration, and the size estimate.
		secs := 0.0
		if gjson.ValidBytes(respBody) {
			for _, path := range []string{"duration", "duration_seconds", "audio_duration", "usage.seconds"} {
				if v := gjson.GetBytes(respBody, path); v.Exists() && v.Type == gjson.Number && v.Float() > 0 {
					secs = v.Float()
					break
				}
			}
		}
		// Estimate duration from body bytes at 16,000 bytes per second, including multipart overhead.
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
		if secs <= 0 {
			secs = elapsed.Seconds()
		}
		if secs <= 0 {
			secs = clientSecs
		}
		if secs <= 0 {
			secs = sizeFloor
		}
		// When the selected duration equals the client value, raise values below half the size or elapsed-time floor.
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
		return &protocol.AudioUsage{Mode: "stt", DurationOrUnits: secs / 3600.0}
	case "realtime":
		mins := elapsed.Minutes()
		if mins <= 0 {
			return nil
		}
		return &protocol.AudioUsage{Mode: "realtime", DurationOrUnits: mins}
	default:
		return nil
	}
}
