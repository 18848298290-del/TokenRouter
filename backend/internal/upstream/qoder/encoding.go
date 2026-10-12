package qoder

import (
	"encoding/base64"
	"strings"
)

const (
	customAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
	stdAlphabet    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	customPad      = '$'
	stdPad         = '='
)

var (
	toCustom [256]byte
	toStd    [256]byte
)

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

func translate(data []byte, table [256]byte) string {
	b := make([]byte, len(data))
	for i, c := range data {
		b[i] = table[c]
	}
	return string(b)
}

func Encode(plaintext []byte) string {
	standard := base64.StdEncoding.EncodeToString(plaintext)
	n := len(standard)
	pivot := n / 3
	rearranged := standard[n-pivot:] + standard[pivot:n-pivot] + standard[:pivot]
	return translate([]byte(rearranged), toCustom)
}

func Decode(encoded string) ([]byte, error) {
	mapped := translate([]byte(encoded), toStd)
	n := len(mapped)
	pivot := n / 3
	standard := mapped[n-pivot:] + mapped[pivot:n-pivot] + mapped[:pivot]
	// Remove padding before raw base64 decoding, including padding inside the reordered input.
	noPad := strings.ReplaceAll(standard, string(stdPad), "")
	return base64.RawStdEncoding.DecodeString(noPad)
}

// DecodeString decodes a COSY string into plaintext.
func DecodeString(encoded string) (string, error) {
	b, err := Decode(encoded)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// EncodeJSON encodes compact JSON bytes using the COSY alphabet.
// The caller supplies already compacted JSON.
func EncodeJSON(compactJSON []byte) string {
	return Encode(compactJSON)
}
