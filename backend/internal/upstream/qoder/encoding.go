package qoder

import (
	"encoding/base64"
	"strings"
)

// COSY body encoding replaces the base64 alphabet and swaps the first and last
// thirds of the encoded text. Decoding reverses that rearrangement before
// parsing base64, so the padding bytes must be removed after their original
// positions have been restored.
//
// The translation tables keep every unrecognized byte unchanged and replace the
// standard alphabet and padding in both directions. A short final base64 group
// may place padding inside the rearranged representation; removing it from the
// restored text lets RawStdEncoding recover the original bytes. EncodeJSON
// delegates this byte transformation and expects callers to prepare compact JSON
// themselves.

// customAlphabet, stdAlphabet, customPad, and stdPad define the reversible
// symbol substitutions used by COSY body encoding.
const (
	customAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
	stdAlphabet    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	customPad      = '$'
	stdPad         = '='
)

// toCustom and toStd hold byte lookup tables initialized once, with unrelated
// bytes mapped to themselves for deterministic translation.
var (
	toCustom [256]byte
	toStd    [256]byte
)

// init builds forward and reverse COSY alphabet lookup tables before encoding
// begins. Every byte initially maps to itself, then alphabet and padding entries
// are replaced; subsequent encoding calls read these tables without mutation.
func init() {
	for i := range 256 {
		toCustom[i] = byte(i)
		toStd[i] = byte(i)
	}
	for i := range len(stdAlphabet) {
		toCustom[stdAlphabet[i]] = customAlphabet[i]
		toStd[customAlphabet[i]] = stdAlphabet[i]
	}
	toCustom[stdPad] = customPad
	toStd[customPad] = stdPad
}

// translate returns a new string after applying the supplied byte translation
// table.
func translate(data []byte, table [256]byte) string {
	b := make([]byte, len(data))
	for i, c := range data {
		b[i] = table[c]
	}
	return string(b)
}

// Encode transforms plaintext into COSY text using base64 alphabet substitution
// and rearrangement. It first builds standard padded base64, then swaps equally
// sized outer thirds while retaining the middle segment. The pivot uses integer
// division so encoded lengths that are not divisible by three retain every byte.
// Substitution runs after the rearrangement, including the padding character,
// and the decoder applies the corresponding inverse sequence.
func Encode(plaintext []byte) string {
	standard := base64.StdEncoding.EncodeToString(plaintext)
	n := len(standard)
	pivot := n / 3
	rearranged := standard[n-pivot:] + standard[pivot:n-pivot] + standard[:pivot]
	return translate([]byte(rearranged), toCustom)
}

// Decode recovers plaintext from COSY text or returns the base64 decoding error.
// It maps the custom alphabet back to standard symbols, swaps the outer thirds
// using the encoded length, and removes padding from the restored
// representation. Padding can occur inside the rearranged input, so trimming its
// original suffix before restoration would leave padding in an invalid position.
// The final raw decoder validates the resulting base64 symbols.
func Decode(encoded string) ([]byte, error) {
	mapped := translate([]byte(encoded), toStd)
	n := len(mapped)
	pivot := n / 3
	standard := mapped[n-pivot:] + mapped[pivot:n-pivot] + mapped[:pivot]
	// Padding moves with the outer thirds during encoding. Remove it from the
	// restored standard representation so RawStdEncoding receives only data
	// symbols.
	noPad := strings.ReplaceAll(standard, string(stdPad), "")
	return base64.RawStdEncoding.DecodeString(noPad)
}

// DecodeString returns plaintext text recovered from an encoded COSY body.
func DecodeString(encoded string) (string, error) {
	b, err := Decode(encoded)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// EncodeJSON applies COSY body encoding to JSON bytes prepared by the caller.
// The caller supplies compact JSON when required by the endpoint; this wrapper
// performs byte encoding and returns the encoded string.
func EncodeJSON(compactJSON []byte) string {
	return Encode(compactJSON)
}
