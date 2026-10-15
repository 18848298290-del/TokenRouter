package qoder

import (
	"crypto/md5"
	"fmt"
)

// signature.go computes Center and Gateway request signatures and formats COSY
// bearer credentials.
// Center signing hashes the Appcode, shared protocol secret, and HTTP date in a
// fixed order. Gateway signing hashes the encoded identity header, encrypted
// temporary key, Unix date, encoded request body, and logical path, so callers
// prepare those exact representations before computing Authorization.

// AppCode, Secret, and Sep define the fixed input format expected by the
// Center's MD5 authentication calculation.
const (
	AppCode = "cosy"
	Secret  = "d2FyLCB3YXIgbmV2ZXIgY2hhbmdlcw=="
	Sep     = "&"
)

// SignCenterRequest returns the MD5 signature for Center or pre-login Appcode
// authentication.
func SignCenterRequest(date string) string {
	return md5Hex(fmt.Sprintf("%s%s%s%s%s", AppCode, Sep, Secret, Sep, date))
}

// SignQoderRequest hashes the prepared COSY header, key, date, body, and logical
// path. Callers supply the encoded body and a path stripped of /algo and query
// parameters, matching the transmitted request representation.
func SignQoderRequest(payloadB64, cosyKey, cosyDate, body, pathWithoutAlgo string) string {
	return md5Hex(fmt.Sprintf("%s\n%s\n%s\n%s\n%s", payloadB64, cosyKey, cosyDate, body, pathWithoutAlgo))
}

// ComposeBearer formats the encoded payload and signature as COSY Authorization
// text.
func ComposeBearer(payloadB64, signature string) string {
	return fmt.Sprintf("Bearer COSY.%s.%s", payloadB64, signature)
}

// md5Hex returns the lowercase hexadecimal MD5 digest of the supplied text.
func md5Hex(value string) string {
	h := md5.Sum([]byte(value))
	return fmt.Sprintf("%x", h[:])
}
