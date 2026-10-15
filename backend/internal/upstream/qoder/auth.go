package qoder

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// auth.go exchanges Qoder Center credentials and prepares machine identities
// and encoded authentication requests.
// Center authentication sends JSON inside a string-valued envelope and then
// applies COSY encoding to the complete body. PAT exchange and token refresh
// share that request shape, while refresh carries the existing security token so
// the server can identify the authenticated session.
//
// Machine identity is prepared before constructing headers. Global requests
// retain separate machine ID, token, and type values; CN machine creation
// produces the UUID-only shape used by its later Gateway flow. Request IDs,
// temporary machine fields, and URL-safe tokens come from the same cryptographic
// random source, whose failure interrupts construction instead of producing a
// predictable credential.

// CenterBaseURL, APIBaseURL, and ClientVersion keep legacy global entry points
// tied to the corresponding site profile constants.
const (
	// CenterBaseURL is the global Center endpoint.
	CenterBaseURL = GlobalCenterBaseURL

	// APIBaseURL is the global inference endpoint.
	APIBaseURL = GlobalGatewayBaseURL

	// ClientVersion is the global COSY client version.
	ClientVersion = GlobalClientVersion
)

// GenerateRequestID returns a random 32-character lowercase hexadecimal request
// identifier. It uses sixteen cryptographic random bytes; crypto/rand.Read
// terminates the process on random-source failure, so callers receive no partial
// identifier.
func GenerateRequestID() string {
	return hex.EncodeToString(mustRandomBytes(16))
}

// RandomToken encodes length cryptographic random bytes using unpadded URL-safe
// base64. Callers supply a nonnegative byte count; zero returns empty text, and
// crypto/rand.Read terminates the process on random-source failure.
func RandomToken(length int) string {
	return base64URLEncode(mustRandomBytes(length))
}

// RandomHex returns exactly length lowercase hexadecimal characters derived from
// random bytes. Callers supply a nonnegative count; odd lengths discard the last
// encoded nibble, and crypto/rand.Read terminates the process on source failure.
func RandomHex(length int) string {
	b := mustRandomBytes((length + 1) / 2)
	// An odd character limit discards the final unused hexadecimal nibble.
	return hex.EncodeToString(b)[:length]
}

// mustRandomBytes allocates length bytes and fills them through
// crypto/rand.Read. Callers supply a nonnegative size. The pinned Go runtime
// terminates the process on random-source failure before the helper can return a
// partial buffer.
func mustRandomBytes(length int) []byte {
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("qoder random bytes: %w", err))
	}
	return b
}

// base64URLEncode encodes bytes with the URL-safe alphabet and omits padding. It
// processes three-byte groups into six-bit symbols, emitting only the symbols
// supported by the final group's actual byte count. Keeping that partial-group
// decision separate from the zero-filled working word prevents trailing zero
// bits from becoming extra token characters. Empty input produces empty text,
// and nonempty output can be used directly in authorization URLs.
func base64URLEncode(b []byte) string {
	// The final three-byte group may be short. Emit symbols only for bytes that
	// exist so zero-filled working bits cannot append extra characters to tokens.
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	var result strings.Builder
	result.Grow((len(b)*8 + 5) / 6)

	for i := 0; i < len(b); i += 3 {
		var val uint32
		remaining := len(b) - i

		val |= uint32(b[i]) << 16
		if remaining > 1 {
			val |= uint32(b[i+1]) << 8
		}
		if remaining > 2 {
			val |= uint32(b[i+2])
		}

		_ = result.WriteByte(alphabet[(val>>18)&0x3f])
		_ = result.WriteByte(alphabet[(val>>12)&0x3f])
		if remaining > 1 {
			_ = result.WriteByte(alphabet[(val>>6)&0x3f])
		}
		if remaining > 2 {
			_ = result.WriteByte(alphabet[val&0x3f])
		}
	}
	return result.String()
}

// NewMachine creates a global machine identity with independent random fields.
// The ID and type use hexadecimal text, while the token encodes fifty random
// bytes; source failure terminates construction through crypto/rand.Read.
func NewMachine() *MachineIdentity {
	return &MachineIdentity{
		MachineID:    RandomHex(36), // Global machine IDs use 36 hexadecimal characters.
		MachineToken: RandomToken(50),
		MachineType:  RandomHex(18),
	}
}

// NewMachineForSite creates the machine shape used by a selected Qoder site. CN
// receives a UUID-like ID with empty token and type values; empty, global, or
// invalid sites select global construction.
func NewMachineForSite(site Site) *MachineIdentity {
	parsed, err := ParseSite(string(site))
	if err == nil && parsed == SiteCN {
		// CN authentication later sends an IDE-shaped machine header set. Create its
		// UUID ID here and leave token and type empty for the Gateway header builder.
		return &MachineIdentity{MachineID: RandomUUIDLike()}
	}
	// Only a parsed CN site selects the UUID-only shape. Other inputs use global
	// construction, matching this helper's legacy default.
	return NewMachine()
}

// ExchangePATContext exchanges a global PAT through the Center job-token
// endpoint. A nil machine, context, doer, or empty Center URL selects
// exchangeJobToken defaults; transport and response failures return to the
// credential owner.
func ExchangePATContext(ctx context.Context, pat string, machine *MachineIdentity, centerURL string, doer RequestDoer) (*AuthIdentity, error) {
	inner := map[string]any{
		"personalToken":      pat,
		"securityOauthToken": "",
		"refreshToken":       "",
		"needRefresh":        false,
		"authInfo":           map[string]any{},
	}
	return exchangeJobToken(ctx, inner, machine, centerURL, "PAT exchange", doer)
}

// RefreshSessionContext refreshes a global COSY identity through the Center
// job-token endpoint. The existing security token is required and both supplied
// tokens are trimmed; context, machine, URL, and transport defaults follow
// exchangeJobToken.
func RefreshSessionContext(ctx context.Context, refreshToken, securityOauthToken string, machine *MachineIdentity, centerURL string, doer RequestDoer) (*AuthIdentity, error) {
	if strings.TrimSpace(securityOauthToken) == "" {
		return nil, fmt.Errorf("qoder: refresh requires securityOauthToken")
	}
	inner := map[string]any{
		"personalToken":      "",
		"securityOauthToken": strings.TrimSpace(securityOauthToken),
		"refreshToken":       strings.TrimSpace(refreshToken),
		"needRefresh":        true,
		"authInfo":           map[string]any{},
	}
	return exchangeJobToken(ctx, inner, machine, centerURL, "refresh", doer)
}

// exchangeJobToken sends one encoded Center authentication operation and returns
// its identity. Empty centerURL selects the global Center, nil machine creates a
// global identity, nil context selects Background, and nil doer creates a
// fifteen-second client. The response body is consumed and closed inside this
// call.
//
// The inner authentication object remains JSON text inside an encodeVersion=1
// envelope before COSY encoding. A successful HTTP response still needs a
// nonempty security token; missing user type selects personal_standard, and
// bounded failure text is redacted before returning.
func exchangeJobToken(ctx context.Context, inner map[string]any, machine *MachineIdentity, centerURL string, operation string, doer RequestDoer) (*AuthIdentity, error) {
	if centerURL == "" {
		centerURL = CenterBaseURL
	}
	if machine == nil {
		machine = NewMachine()
	}

	// PAT and refresh callers supply JSON-valued string, boolean, and map fields.
	// Keep their JSON as the envelope's payload string before COSY encoding.
	innerJSON, _ := json.Marshal(inner)
	outer := map[string]any{
		"payload":       string(innerJSON),
		"encodeVersion": "1",
	}
	outerJSON, _ := json.Marshal(outer)

	req, err := newCenterEncodedRequest("POST", centerURL+"/algo/api/v3/user/jobToken?Encode=1", outerJSON, machine)
	if err != nil {
		return nil, fmt.Errorf("qoder: %s request: %w", operation, err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req = req.WithContext(ctx)

	if doer == nil {
		client := &http.Client{Timeout: 15 * time.Second}
		doer = client.Do
	}
	resp, err := doer(req)
	if err != nil {
		return nil, fmt.Errorf("qoder: %s request: %w", operation, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// A rejected response still yields its HTTP status when diagnostic reading
		// fails. Use the bounded fragment and redact it before returning
		// authentication failure.
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("qoder: %s failed with status %d: %s", operation, resp.StatusCode, RedactSensitiveText(string(bodyBytes)))
	}

	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("qoder: parse %s response: %w", operation, err)
	}

	token, _ := data["securityOauthToken"].(string)
	refreshToken, _ := data["refreshToken"].(string)
	name, _ := data["name"].(string)
	uid, _ := data["id"].(string)
	userType, _ := data["userType"].(string)
	if userType == "" {
		userType = "personal_standard"
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("qoder: %s response missing securityOauthToken", operation)
	}

	return &AuthIdentity{
		Name:               name,
		AID:                uid,
		UID:                uid,
		UserType:           userType,
		SecurityOauthToken: strings.TrimSpace(token),
		RefreshToken:       strings.TrimSpace(refreshToken),
	}, nil
}

// newCenterEncodedRequest builds an encoded Center request with machine and
// Appcode headers. Callers provide a usable machine identity; the returned
// request has no caller context until exchangeJobToken attaches one before
// transport.
func newCenterEncodedRequest(method, rawURL string, payload []byte, machine *MachineIdentity) (*http.Request, error) {
	body := EncodeJSON(payload)
	req, err := http.NewRequest(method, rawURL, bytes.NewReader([]byte(body)))
	if err != nil {
		return nil, err
	}
	date := time.Now().UTC().Format(http.TimeFormat)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "Go-http-client/2.0")
	req.Header.Set("cosy-machinetoken", machine.MachineToken)
	req.Header.Set("cosy-machinetype", machine.MachineType)
	req.Header.Set("cosy-machineid", machine.MachineID)
	req.Header.Set("cosy-version", ClientVersion)
	req.Header.Set("cosy-clienttype", "5")
	req.Header.Set("login-version", "v2")
	req.Header.Set("appcode", AppCode)
	req.Header.Set("Date", date)
	req.Header.Set("signature", SignCenterRequest(date))
	return req, nil
}

// pathWithoutAlgo returns the URL path with its leading /algo prefix removed.
// Signing excludes query parameters; an unparseable URL returns the supplied
// text so callers retain a deterministic signing input.
func pathWithoutAlgo(rawURL string) string {
	u, err := url.Parse(rawURL)
	// URL parsing failure leaves the raw signing input available. Returning it as
	// supplied keeps this helper from inventing a different path representation.
	if err != nil {
		return rawURL
	}
	return strings.TrimPrefix(u.Path, "/algo")
}
