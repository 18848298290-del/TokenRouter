package media

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// Video status output can expose a signed or provider-protected content URL
// that the gateway replaces with a task proxy URL. A nonblank root
// video.url string is replaced directly, while nested string values use the
// supplied task matcher, so that known response field does not depend on
// matcher qualification.
//
// JSON is decoded with UseNumber before traversal so reencoding retains
// numeric spellings that float64 conversion could round. Changed output is
// marshaled from maps and slices, which can reorder members and collapse
// duplicate object keys; untouched or rejected input returns the shared
// source bytes. Recursive child replacement writes changed scalar
// interfaces back into their parent collection, alongside nested
// maps/slices already mutated during traversal.

// RewriteVideoContentURLs returns proxy-rewritten JSON when known or
// matched video content strings are found. Empty/invalid body or blank
// requestID/proxyURL returns the shared input; the strings are trimmed for
// blank checks but passed/replaced as supplied.
//
// The nonblank root video.url is replaced unconditionally, including when
// already equal to proxyURL. Other strings require a usable match callback
// whenever traversal reaches them, and match receives the supplied
// requestID. UseNumber retains numeric encoding during a changed-body
// marshal, while object order/duplicate-member layout can change. Decode or
// marshal failure returns the input, and a no-match traversal also returns
// its shared storage.
func RewriteVideoContentURLs(body []byte, requestID, proxyURL string, match func(string, string) bool) []byte {
	if len(body) == 0 || strings.TrimSpace(requestID) == "" || strings.TrimSpace(proxyURL) == "" || !gjson.ValidBytes(body) {
		return body
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	// Keep number tokens as json.Number during compatibility rewriting, so
	// large integers and decimal spellings survive any required object
	// reencoding without float64 conversion.
	decoder.UseNumber()
	var value any
	// If decoding still rejects otherwise validated input, retain its bytes
	// for caller handling rather than publishing a partial proxy response from
	// incomplete decoded state.
	if err := decoder.Decode(&value); err != nil {
		return body
	}
	changed := rewriteGrokMediaKnownVideoURL(&value, proxyURL)
	if rewriteGrokMediaVideoContentURLValue(&value, requestID, proxyURL, match) {
		changed = true
	}
	// When traversal found no replacement, return the existing byte storage so
	// a no-op request keeps its formatting and duplicate-member layout
	// untouched.
	if !changed {
		return body
	}
	rewritten, err := json.Marshal(value)
	// If encoding the changed tree fails, retain the original response bytes
	// because partial rewritten JSON cannot serve as a complete status
	// payload.
	if err != nil {
		return body
	}
	return rewritten
}

// rewriteGrokMediaKnownVideoURL replaces a nonblank root video.url string
// and reports that operation. Nil input, nonobject root/video or
// nonstring/blank URL returns false; true is returned even when the
// existing string equals proxyURL.
func rewriteGrokMediaKnownVideoURL(value *any, proxyURL string) bool {
	if value == nil {
		return false
	}
	root, ok := (*value).(map[string]any)
	if !ok {
		return false
	}
	video, ok := root["video"].(map[string]any)
	if !ok {
		return false
	}
	rawURL, ok := video["url"].(string)
	if !ok || strings.TrimSpace(rawURL) == "" {
		return false
	}
	// Treat the known root video.url field as content output independently of
	// the recursive matcher, installing the supplied proxy even when that
	// field already equals it.
	video["url"] = proxyURL
	return true
}

// rewriteGrokMediaVideoContentURLValue mutates matched strings throughout a
// decoded JSON tree and reports any replacement. Nil input returns false,
// and match is required when a string is reached; decoding supplies acyclic
// maps and slices.
//
// A map or slice child is first held in a local interface value. Successful
// recursion writes that value back to the parent so replaced scalar strings
// are installed alongside nested-reference mutations. All string
// qualification delegates to match with requestID, keeping unrelated string
// values while proxyURL is inserted exactly as supplied.
func rewriteGrokMediaVideoContentURLValue(value *any, requestID, proxyURL string, match func(string, string) bool) bool {
	if value == nil {
		return false
	}
	switch typed := (*value).(type) {
	case map[string]any:
		changed := false
		// Write a successfully replaced local interface back into its map entry,
		// so string replacements survive the interface copy used for recursion.
		for key, child := range typed {
			childValue := child
			if rewriteGrokMediaVideoContentURLValue(&childValue, requestID, proxyURL, match) {
				typed[key] = childValue
				changed = true
			}
		}
		return changed
	case []any:
		changed := false
		// Reinstall changed slice children after recursion, carrying scalar
		// replacements into the original sequence while nested collections keep
		// their referenced traversal changes.
		for index, child := range typed {
			childValue := child
			if rewriteGrokMediaVideoContentURLValue(&childValue, requestID, proxyURL, match) {
				typed[index] = childValue
				changed = true
			}
		}
		return changed
	case string:
		if match(typed, requestID) {
			*value = proxyURL
			return true
		}
	}
	return false
}
