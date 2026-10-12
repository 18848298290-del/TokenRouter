package qoder

import (
	"crypto/md5"
	"fmt"
)

const (
	AppCode = "cosy"
	Secret  = "d2FyLCB3YXIgbmV2ZXIgY2hhbmdlcw=="
	Sep     = "&"
)

// SignCenterRequest generates the MD5 signature used for Center and Appcode requests.
func SignCenterRequest(date string) string {
	return md5Hex(fmt.Sprintf("%s%s%s%s%s", AppCode, Sep, Secret, Sep, date))
}

// SignQoderRequest generates the COSY gateway MD5 signature.
func SignQoderRequest(payloadB64, cosyKey, cosyDate, body, pathWithoutAlgo string) string {
	return md5Hex(fmt.Sprintf("%s\n%s\n%s\n%s\n%s", payloadB64, cosyKey, cosyDate, body, pathWithoutAlgo))
}

// ComposeBearer builds the COSY Authorization header value.
func ComposeBearer(payloadB64, signature string) string {
	return fmt.Sprintf("Bearer COSY.%s.%s", payloadB64, signature)
}

func md5Hex(value string) string {
	h := md5.Sum([]byte(value))
	return fmt.Sprintf("%x", h[:])
}
