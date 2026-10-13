package payment

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/apperror"
)

// Payment continuation carries a local order session or a WeChat
// payer/purchase context through a signed two-part token. JSON payload bytes
// are base64url encoded and that encoded text is HMAC-SHA256 signed;
// verification precedes JSON interpretation, and the public parsers still
// require a primary signing key even when legacy verification keys exist.
//
// Order return URLs accept the fixed internal result path and an allowed
// host/port, then discard caller query/fragment before server order parameters
// are added. Host matching compares host/port rather than URL scheme or
// default-port equivalence. Token creation defaults absent timestamps, while
// expiry validation permits nonpositive expiration and accepts the exact
// current second; IssuedAt is not a freshness check. Saved order identity
// matching and purchase validation belong to the consuming workflow after
// token parsing.

// Resume constants define payment return navigation, visible method source
// settings, token identities, configuration errors and default lifetimes used
// by creation and parsing.
const (
	// fixed internal result page.
	ResumePaymentResultReturnPath = "/payment/result"

	// hosted payment redirect source.
	PaymentSourceHostedRedirect = "hosted_redirect"
	// WeChat payer continuation source.
	PaymentSourceWechatInAppResume = "wechat_in_app_resume"

	// Alipay provider choice setting.
	SettingPaymentVisibleMethodAlipaySource = "payment_visible_method_alipay_source"
	// WeChat provider choice setting.
	SettingPaymentVisibleMethodWxpaySource = "payment_visible_method_wxpay_source"
	// Alipay visible exposure setting.
	SettingPaymentVisibleMethodAlipayEnabled = "payment_visible_method_alipay_enabled"
	// WeChat visible exposure setting.
	SettingPaymentVisibleMethodWxpayEnabled = "payment_visible_method_wxpay_enabled"

	// official Alipay source label.
	VisibleMethodSourceOfficialAlipay = "official_alipay"
	// EasyPay Alipay source label.
	VisibleMethodSourceEasyPayAlipay = "easypay_alipay"
	// official WeChat source label.
	VisibleMethodSourceOfficialWechat = "official_wxpay"
	// EasyPay WeChat source label.
	VisibleMethodSourceEasyPayWechat = "easypay_wxpay"

	// WeChat continuation discriminator label.
	ResumeWechatPaymentResumeTokenType = "wechat_payment_resume"

	// missing signing configuration reason.
	ResumePaymentResumeNotConfiguredCode = "PAYMENT_RESUME_NOT_CONFIGURED"
	// missing signing configuration message.
	ResumePaymentResumeNotConfiguredMessage = "payment resume tokens require a configured signing key"

	// default order session lifetime.
	ResumePaymentResumeTokenTTL = 24 * time.Hour
	// default payer continuation lifetime.
	ResumeWechatPaymentResumeTokenTTL = 15 * time.Minute
)

// ResumeTokenClaims records signed order identity, optional association checks
// and session timestamps.
type ResumeTokenClaims struct {
	// OrderID is required to be positive by token creation/parsing. Public
	// resolution uses this local identity before comparing any supplied optional
	// user/provider/method claims.
	OrderID int64 `json:"oid"`
	// UserID is optional signed association data. Public resolution compares it
	// only when positive, so zero omits an additional buyer check rather than
	// naming an authenticated requester.
	UserID int64 `json:"uid,omitempty"`
	// ProviderInstanceID is optional signed merchant association text. Public
	// resolution compares nonempty signed text with its resolved saved instance
	// identity after reading the order.
	ProviderInstanceID string `json:"pi,omitempty"`
	// ProviderKey is optional signed provider association text. Public resolution
	// compares nonempty signed text using saved snapshot/column precedence and
	// case-insensitive key rule.
	ProviderKey string `json:"pk,omitempty"`
	// PaymentType is optional signed method association text. Public resolution
	// compares nonempty values after NormalizeVisibleMethod trims and maps
	// compatibility labels, independently of provider key.
	PaymentType string `json:"pt,omitempty"`
	// CanonicalReturnURL records the destination selected during initiation. It
	// is signed with the claims, but current public order lookup does not use
	// this field as an association check.
	CanonicalReturnURL string `json:"ru,omitempty"`
	// IssuedAt records Unix seconds, defaulting to current time only when zero on
	// creation. Parsing retains its supplied value and performs no future-issue
	// or age check.
	IssuedAt int64 `json:"iat"`
	// ExpiresAt is Unix seconds, defaulting to the order token lifetime only when
	// zero on creation. Parsing checks positive values; nonpositive expiry
	// remains accepted.
	ExpiresAt int64 `json:"exp,omitempty"`
}

// WeChatPaymentResumeClaims carries signed payer identity and purchase inputs
// for checkout continuation.
type WeChatPaymentResumeClaims struct {
	// TokenType is overwritten with the WeChat continuation discriminator on
	// creation. Parsing requires that literal before treating the payload as
	// payer/purchase context.
	TokenType string `json:"tk,omitempty"`
	// OpenID is trimmed and required nonblank during creation and parsing. HTTP
	// continuation application then uses that signed payer identity to replace
	// checkout input.
	OpenID string `json:"openid"`
	// PaymentType is normalized when the visible method helper returns nonempty
	// and defaults to WeChat when blank. This token parser does not independently
	// restrict all accepted method spellings.
	PaymentType string `json:"pt,omitempty"`
	// Amount retains purchase text as signed input without numeric validation
	// here. The HTTP continuation helper parses its nonblank value before runtime
	// purchase checks.
	Amount string `json:"amt,omitempty"`
	// OrderType defaults blank input to balance during creation/parsing. Other
	// text remains in claims for checkout's purchase handling rather than a
	// closed enum validation here.
	OrderType string `json:"ot,omitempty"`
	// PlanID is signed purchase identity as supplied. HTTP continuation replaces
	// checkout plan selection only for a positive value; this service adds no
	// positivity check.
	PlanID int64 `json:"pid,omitempty"`
	// RedirectTo retains the supplied frontend navigation text in signed claims.
	// The OAuth caller sanitizes it separately before creating this token;
	// parsing does not validate the path here.
	RedirectTo string `json:"rd,omitempty"`
	// Scope retains the caller's supplied authorization scope in signed claims.
	// The OAuth handler normalizes its cookie choice and can replace it with
	// nonblank provider scope.
	Scope string `json:"scp,omitempty"`
	// IssuedAt is Unix seconds and defaults to current time when zero on
	// creation. Parsing retains its value without validating future issue time or
	// elapsed age.
	IssuedAt int64 `json:"iat"`
	// ExpiresAt is Unix seconds and defaults to the payer token lifetime when
	// zero on creation. Parsing rejects past positive values while accepting
	// nonpositive ones.
	ExpiresAt int64 `json:"exp,omitempty"`
}

// PaymentResumeService signs continuation tokens and verifies against copied
// primary/legacy key material.
type PaymentResumeService struct {
	// signingKey is copied from nonempty constructor input and also becomes the
	// first verification key. Public creation/parsing requires this primary key
	// even when verifyKeys contains fallbacks.
	signingKey []byte
	// verifyKeys holds independent nonempty distinct key copies in
	// primary/fallback order. Token verification tries each in that order while
	// callers keep service internals immutable after construction.
	verifyKeys [][]byte
}

// NewPaymentResumeService copies primary and nonempty distinct fallback keys
// for continuation signing and verification. The primary copy is also the
// first verification key, while duplicate byte values are omitted.
//
// Copying detaches service key storage from caller mutation, and verification
// order follows primary then accepted fallback inputs. A service created with
// only fallbacks can hold verification material, but its public parsers call
// EnsureSigningKey and reject the absent primary. That gate remains separate
// from the private signature loop, so constructor acceptance does not
// establish parsing readiness.
func NewPaymentResumeService(signingKey []byte, verifyFallbacks ...[]byte) *PaymentResumeService {
	svc := &PaymentResumeService{}
	if len(signingKey) > 0 {
		svc.signingKey = append([]byte(nil), signingKey...)
		svc.verifyKeys = append(svc.verifyKeys, svc.signingKey)
	}
	// Copy each nonempty distinct legacy key before retaining it alongside the
	// primary copy. Caller-owned byte mutations then cannot change later
	// verification, and duplicate values need only one signature trial.
	for _, fallback := range verifyFallbacks {
		if len(fallback) == 0 {
			continue
		}
		cloned := append([]byte(nil), fallback...)
		duplicate := false
		for _, existing := range svc.verifyKeys {
			if bytes.Equal(existing, cloned) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			svc.verifyKeys = append(svc.verifyKeys, cloned)
		}
	}
	return svc
}

// IsSigningConfigured reports whether a nonnil service has a nonempty primary
// signing key.
func (s *PaymentResumeService) IsSigningConfigured() bool {
	return s != nil && len(s.signingKey) > 0
}

// EnsureSigningKey reports service-unavailable when no primary signing key is
// configured. Both public creation and parsing call this gate, so legacy
// verification keys alone do not enable those operations.
func (s *PaymentResumeService) EnsureSigningKey() error {
	if s.IsSigningConfigured() {
		return nil
	}
	return infraerrors.ServiceUnavailable(ResumePaymentResumeNotConfiguredCode, ResumePaymentResumeNotConfiguredMessage)
}

// NormalizeVisibleMethod trims the label and delegates legacy provider-prefix
// mapping to GetBasePaymentType. Unknown labels retain that helper's result,
// so this normalization does not establish that an enabled or supported method
// exists.
func NormalizeVisibleMethod(method string) string {
	return GetBasePaymentType(strings.TrimSpace(method))
}

// NormalizeVisibleMethods returns first-seen normalized nonblank method labels
// in a new slice, dropping aliases/duplicates according to exact normalized
// text. Empty input returns nil; caller storage remains unchanged and
// supported/enabled validation occurs separately.
func NormalizeVisibleMethods(methods []string) []string {
	if len(methods) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(methods))
	out := make([]string, 0, len(methods))
	for _, method := range methods {
		normalized := NormalizeVisibleMethod(method)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

// NormalizePaymentSource lowercases and trims source text, defaulting blank to
// hosted_redirect and mapping the listed WeChat aliases. Other labels retain
// normalized text so callers can apply their own source validation.
func NormalizePaymentSource(source string) string {
	// Map the retained WeChat source aliases to one continuation label before
	// creation auditing and route handling. Unknown normalized source text
	// remains available for caller-specific decisions.
	switch strings.TrimSpace(strings.ToLower(source)) {
	case "", PaymentSourceHostedRedirect:
		return PaymentSourceHostedRedirect
	case "wechat_in_app", "wxpay_resume", PaymentSourceWechatInAppResume:
		return PaymentSourceWechatInAppResume
	default:
		return strings.TrimSpace(strings.ToLower(source))
	}
}

// NormalizeVisibleMethodSource maps recognized official/EasyPay source aliases
// for normalized Alipay or WeChat methods. Missing, unsupported or
// method-incompatible source text returns empty; callers distinguish that
// absence from a configured invalid source through their own raw-input checks.
func NormalizeVisibleMethodSource(method, source string) string {
	// Interpret provider aliases in the requested visible method's namespace so
	// the same official/EasyPay text selects its Alipay or WeChat source rather
	// than an unrelated method.
	switch NormalizeVisibleMethod(method) {
	case TypeAlipay:
		switch strings.TrimSpace(strings.ToLower(source)) {
		case VisibleMethodSourceOfficialAlipay, TypeAlipay, TypeAlipayDirect, "official":
			return VisibleMethodSourceOfficialAlipay
		case VisibleMethodSourceEasyPayAlipay, TypeEasyPay:
			return VisibleMethodSourceEasyPayAlipay
		}
	case TypeWxpay:
		switch strings.TrimSpace(strings.ToLower(source)) {
		case VisibleMethodSourceOfficialWechat, TypeWxpay, TypeWxpayDirect, "wechat", "official":
			return VisibleMethodSourceOfficialWechat
		case VisibleMethodSourceEasyPayWechat, TypeEasyPay:
			return VisibleMethodSourceEasyPayWechat
		}
	}
	return ""
}

// VisibleMethodProviderKeyForSource returns the provider key and a
// recognized-source result for a normalized visible method/source pair.
// Unsupported pairs return empty,false, and the successful result does not
// inspect any provider instance availability.
func VisibleMethodProviderKeyForSource(method, source string) (string, bool) {
	switch NormalizeVisibleMethodSource(method, source) {
	case VisibleMethodSourceOfficialAlipay:
		return TypeAlipay, NormalizeVisibleMethod(method) == TypeAlipay
	case VisibleMethodSourceEasyPayAlipay:
		return TypeEasyPay, NormalizeVisibleMethod(method) == TypeAlipay
	case VisibleMethodSourceOfficialWechat:
		return TypeWxpay, NormalizeVisibleMethod(method) == TypeWxpay
	case VisibleMethodSourceEasyPayWechat:
		return TypeEasyPay, NormalizeVisibleMethod(method) == TypeWxpay
	default:
		return "", false
	}
}

// ResumeVisibleMethodEnabledSettingKey returns the Alipay/WeChat exposure
// setting key for a normalized method. Other labels return empty, so settings
// callers apply exposure rules only to the two supported visible method keys.
func ResumeVisibleMethodEnabledSettingKey(method string) string {
	switch NormalizeVisibleMethod(method) {
	case TypeAlipay:
		return SettingPaymentVisibleMethodAlipayEnabled
	case TypeWxpay:
		return SettingPaymentVisibleMethodWxpayEnabled
	default:
		return ""
	}
}

// ResumeVisibleMethodSourceSettingKey returns the Alipay/WeChat source-choice
// setting key for a normalized method. Other labels return empty, leaving
// provider-specific methods outside that pair of configurable source labels.
func ResumeVisibleMethodSourceSettingKey(method string) string {
	switch NormalizeVisibleMethod(method) {
	case TypeAlipay:
		return SettingPaymentVisibleMethodAlipaySource
	case TypeWxpay:
		return SettingPaymentVisibleMethodWxpaySource
	default:
		return ""
	}
}

// CanonicalizeReturnURL validates an optional absolute HTTP/HTTPS result
// destination and rebuilds it from accepted scheme/host and the fixed internal
// path. Blank input returns empty,nil; malformed authority/path or disallowed
// host returns INVALID_RETURN_URL.
//
// Userinfo and host parameter delimiters are rejected before host comparison
// because provider return fields can enter signature input. Request host or
// parsed referer host can authorize host/port using ResumeSameOriginHost,
// which does not compare schemes or default-port equivalence. Rebuilding
// removes caller query, fragment and encoded path spelling;
// ResumeBuildPaymentReturnURL adds server order/session parameters afterward.
// Callers retain that separation before passing a redirect to payment
// adapters.
// @project-doc docs/domains/payments_and_entitlements.md#callback_security
func CanonicalizeReturnURL(raw string, srcHost string, srcURL string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return "", infraerrors.BadRequest("INVALID_RETURN_URL", "return_url must be an absolute http/https URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", infraerrors.BadRequest("INVALID_RETURN_URL", "return_url must use http or https")
	}
	if parsed.User != nil {
		return "", infraerrors.BadRequest("INVALID_RETURN_URL", "return_url must not contain user information")
	}
	// URL parsing can accept host delimiter text that later enters provider
	// signature input. Reject those parameter separators before matching the site
	// host so authority text cannot supply extra signature fields.
	if strings.ContainsAny(parsed.Host, "&=") {
		return "", infraerrors.BadRequest("INVALID_RETURN_URL", "return_url host contains invalid characters")
	}
	if parsed.Path != ResumePaymentResultReturnPath {
		return "", infraerrors.BadRequest("INVALID_RETURN_URL", "return_url must target the canonical internal payment result page")
	}
	if !ResumeAllowedReturnURLHost(parsed.Host, srcHost, srcURL) {
		return "", infraerrors.BadRequest("INVALID_RETURN_URL", "return_url must use the same host as the current site or browser origin")
	}
	// Rebuild the accepted scheme, host and fixed path so user query/fragment and
	// alternate encoded path spelling disappear together. Server order/session
	// values are added only after this canonical result returns.
	canonical := url.URL{
		Scheme: parsed.Scheme,
		Host:   parsed.Host,
		Path:   ResumePaymentResultReturnPath,
	}
	return canonical.String(), nil
}

// ResumeAllowedReturnURLHost accepts a host/port matching the request host or
// a parsed nonblank referer host. Unreadable/hostless referer returns false;
// scheme and default-port normalization are not part of this host comparison.
func ResumeAllowedReturnURLHost(returnURLHost string, requestHost string, refererURL string) bool {
	if ResumeSameOriginHost(returnURLHost, requestHost) {
		return true
	}

	refererURL = strings.TrimSpace(refererURL)
	if refererURL == "" {
		return false
	}
	// The request host did not match, so try the browser referer's parsed host as
	// the second site observation. Its scheme contributes no additional origin
	// comparison here.
	parsedReferer, err := url.Parse(refererURL)
	if err != nil || parsedReferer.Host == "" {
		return false
	}
	return ResumeSameOriginHost(returnURLHost, parsedReferer.Host)
}

// ResumeBuildPaymentReturnURL adds server order/session result parameters to a
// supplied absolute base URL and removes its fragment. Blank base returns
// empty,nil; callers first apply CanonicalizeReturnURL to enforce payment
// path/scheme/authority rules.
//
// A positive order ID and nonblank merchant/token values replace their named
// query entries, while status is set to success. Existing other base query
// entries remain, so the canonicalization caller is what removes
// user-controlled parameters. Query encoding carries these identities into
// adapter redirect fields without conflating the merchant number with the
// local numeric ID or signed session credential.
func ResumeBuildPaymentReturnURL(base string, orderID int64, outTradeNo string, resumeToken string) (string, error) {
	canonical := strings.TrimSpace(base)
	if canonical == "" {
		return "", nil
	}

	parsed, err := url.Parse(canonical)
	if err != nil {
		return "", infraerrors.BadRequest("INVALID_RETURN_URL", "return_url must be a valid URL")
	}
	if !parsed.IsAbs() || parsed.Host == "" {
		return "", infraerrors.BadRequest("INVALID_RETURN_URL", "return_url must be a valid absolute URL")
	}
	// Keep fragment text out of provider return fields before adding local order,
	// merchant number and optional session credential. Other query removal
	// belongs to the preceding canonicalization operation.
	parsed.Fragment = ""

	query := parsed.Query()
	if orderID > 0 {
		query.Set("order_id", strconv.FormatInt(orderID, 10))
	}
	if strings.TrimSpace(outTradeNo) != "" {
		query.Set("out_trade_no", strings.TrimSpace(outTradeNo))
	}
	if strings.TrimSpace(resumeToken) != "" {
		query.Set("resume_token", strings.TrimSpace(resumeToken))
	}
	query.Set("status", "success")
	parsed.RawQuery = query.Encode()

	return parsed.String(), nil
}

// ResumeSameOriginHost compares trimmed nonempty host/port text
// case-insensitively for names and exactly for parsed port strings. An exact
// case-insensitive raw match succeeds first; otherwise SplitHostPort results
// are compared. URL scheme, DNS equivalence and implicit default-port
// expansion are outside this comparison.
func ResumeSameOriginHost(returnURLHost string, requestHost string) bool {
	returnHost := strings.TrimSpace(returnURLHost)
	reqHost := strings.TrimSpace(requestHost)
	if returnHost == "" || reqHost == "" {
		return false
	}
	if strings.EqualFold(returnHost, reqHost) {
		return true
	}

	returnName, returnPort := ResumeSplitHostPortDefault(returnHost)
	reqName, reqPort := ResumeSplitHostPortDefault(reqHost)
	if returnName == "" || reqName == "" {
		return false
	}
	return strings.EqualFold(returnName, reqName) && returnPort == reqPort
}

// ResumeSplitHostPortDefault returns SplitHostPort's host and port when
// parsing succeeds, otherwise the raw supplied text with empty port. The
// caller trims input first; this fallback performs no default-port inference.
func ResumeSplitHostPortDefault(raw string) (string, string) {
	if host, port, err := net.SplitHostPort(raw); err == nil {
		return host, port
	}
	// A host lacking a successfully parsed port keeps its raw name with empty
	// port. That supports host-only comparison while retaining explicit-port
	// distinctions rather than inferring scheme defaults.
	return raw, ""
}

// CreateToken signs a positive local order ID and the caller's optional
// association/destination claims. It requires a configured primary key and
// defaults zero IssuedAt and ExpiresAt using independent current-time reads.
// Negative/nonzero supplied timestamps remain as given; consuming lookup
// validates saved order associations after parsing.
func (s *PaymentResumeService) CreateToken(claims ResumeTokenClaims) (string, error) {
	if err := s.EnsureSigningKey(); err != nil {
		return "", err
	}
	if claims.OrderID <= 0 {
		return "", fmt.Errorf("resume token requires order id")
	}
	if claims.IssuedAt == 0 {
		claims.IssuedAt = time.Now().Unix()
	}
	if claims.ExpiresAt == 0 {
		claims.ExpiresAt = time.Now().Add(ResumePaymentResumeTokenTTL).Unix()
	}
	return s.createSignedToken(claims)
}

// ParseToken verifies an order-session signature, decodes claims and requires
// a positive order ID before expiry checking. A primary signing key remains
// required even when fallback verification keys were copied.
//
// Payload/signature errors become INVALID_RESUME_TOKEN, and positive
// expiration is rejected only after its Unix second has passed. Nonpositive
// expiration is accepted, IssuedAt is not checked, and optional
// merchant/user/method values are not matched against an order here. Public
// lookup performs those association checks after reading the stored order,
// retaining token identity separately from provider callback validation.
func (s *PaymentResumeService) ParseToken(token string) (*ResumeTokenClaims, error) {
	if err := s.EnsureSigningKey(); err != nil {
		return nil, err
	}
	var claims ResumeTokenClaims
	// Collapse signature/base64/JSON failures into the order session's public
	// invalid-payload response. Callers receive that reason irrespective of which
	// shared decoding stage failed.
	if err := s.parseSignedToken(token, &claims); err != nil {
		return nil, infraerrors.BadRequest("INVALID_RESUME_TOKEN", "resume token payload is invalid")
	}
	if claims.OrderID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_RESUME_TOKEN", "resume token missing order id")
	}
	if err := ResumeValidatePaymentResumeExpiry(claims.ExpiresAt, "INVALID_RESUME_TOKEN", "resume token has expired"); err != nil {
		return nil, err
	}
	return &claims, nil
}

// CreateWeChatPaymentResumeToken signs trimmed nonblank OpenID with
// payer/purchase continuation claims using the primary key. Zero timestamps
// receive their independent current-time defaults; nonzero values remain as
// supplied.
//
// The method is normalized when possible and defaults blank to WeChat, order
// type defaults blank to balance, and token type is overwritten with the
// WeChat discriminator. Amount, plan, redirect and scope remain supplied
// values rather than validated purchase inputs. That signed context lets HTTP
// continuation carry payer identity into later authenticated checkout, whose
// account ownership and purchase validation remain separate.
func (s *PaymentResumeService) CreateWeChatPaymentResumeToken(claims WeChatPaymentResumeClaims) (string, error) {
	if err := s.EnsureSigningKey(); err != nil {
		return "", err
	}
	claims.OpenID = strings.TrimSpace(claims.OpenID)
	if claims.OpenID == "" {
		return "", fmt.Errorf("wechat payment resume token requires openid")
	}
	if claims.IssuedAt == 0 {
		claims.IssuedAt = time.Now().Unix()
	}
	if claims.ExpiresAt == 0 {
		claims.ExpiresAt = time.Now().Add(ResumeWechatPaymentResumeTokenTTL).Unix()
	}
	if normalized := NormalizeVisibleMethod(claims.PaymentType); normalized != "" {
		claims.PaymentType = normalized
	}
	if claims.PaymentType == "" {
		claims.PaymentType = TypeWxpay
	}
	if claims.OrderType == "" {
		claims.OrderType = OrderTypeBalance
	}
	claims.TokenType = ResumeWechatPaymentResumeTokenType
	return s.createSignedToken(claims)
}

// ParseWeChatPaymentResumeToken verifies signed payer context and requires the
// WeChat token discriminator and trimmed nonblank OpenID. Primary-key
// readiness is checked before private verification can try fallback keys.
//
// Positive past expiration is rejected, but nonpositive expiration and
// unchecked IssuedAt remain accepted. Method normalization and blank defaults
// mirror creation, while amount/plan/redirect/scope are returned for their
// consumers to validate. Parsing does not create an order or establish buyer
// authentication; the HTTP handler retains its authenticated account while
// applying this payer/purchase continuation.
func (s *PaymentResumeService) ParseWeChatPaymentResumeToken(token string) (*WeChatPaymentResumeClaims, error) {
	if err := s.EnsureSigningKey(); err != nil {
		return nil, err
	}
	var claims WeChatPaymentResumeClaims
	// Collapse shared decoder failures into the WeChat continuation's public
	// invalid-payload response. Its later discriminator/OpenID/expiry checks keep
	// their separate contextual errors after successful decoding.
	if err := s.parseSignedToken(token, &claims); err != nil {
		return nil, infraerrors.BadRequest("INVALID_WECHAT_PAYMENT_RESUME_TOKEN", "wechat payment resume token payload is invalid")
	}
	if claims.TokenType != ResumeWechatPaymentResumeTokenType {
		return nil, infraerrors.BadRequest("INVALID_WECHAT_PAYMENT_RESUME_TOKEN", "wechat payment resume token type mismatch")
	}
	claims.OpenID = strings.TrimSpace(claims.OpenID)
	if claims.OpenID == "" {
		return nil, infraerrors.BadRequest("INVALID_WECHAT_PAYMENT_RESUME_TOKEN", "wechat payment resume token missing openid")
	}
	if err := ResumeValidatePaymentResumeExpiry(claims.ExpiresAt, "INVALID_WECHAT_PAYMENT_RESUME_TOKEN", "wechat payment resume token has expired"); err != nil {
		return nil, err
	}
	if normalized := NormalizeVisibleMethod(claims.PaymentType); normalized != "" {
		claims.PaymentType = normalized
	}
	if claims.PaymentType == "" {
		claims.PaymentType = TypeWxpay
	}
	if claims.OrderType == "" {
		claims.OrderType = OrderTypeBalance
	}
	return &claims, nil
}

// createSignedToken serializes claims as JSON, base64url encodes those bytes
// and appends a signature over the encoded payload text. Callers apply
// readiness and claim validation before this helper; marshal errors return
// with context.
func (s *PaymentResumeService) createSignedToken(claims any) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal resume claims: %w", err)
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	return encodedPayload + "." + s.sign(encodedPayload), nil
}

// parseSignedToken requires two nonempty token parts, verifies the encoded
// payload text and then decodes JSON into dest. Callers provide a suitable
// destination pointer and apply token-specific claims afterward.
//
// Signature comparison occurs before payload decoding, so malformed untrusted
// JSON is not interpreted until one copied key accepts the token. No type,
// expiry or association checks happen in this shared decoder. Public parsers
// convert its errors to their own token reason codes, while this helper
// retains the ordered signature/base64/JSON acceptance stages.
func (s *PaymentResumeService) parseSignedToken(token string, dest any) error {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return infraerrors.BadRequest("INVALID_RESUME_TOKEN", "resume token is malformed")
	}
	// Authenticate the encoded payload before interpreting its decoded bytes as
	// claims. Token-specific field checks follow in the public parser after this
	// shared signature and JSON stage.
	if !s.verifySignature(parts[0], parts[1]) {
		return infraerrors.BadRequest("INVALID_RESUME_TOKEN", "resume token signature mismatch")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return infraerrors.BadRequest("INVALID_RESUME_TOKEN", "resume token payload is malformed")
	}
	return json.Unmarshal(payload, dest)
}

// verifySignature compares the supplied encoded signature against each
// accepted key's canonical base64url HMAC text in order. Nil service or no
// match returns false; input signature text is compared with hmac.Equal rather
// than decoded into alternative spellings.
func (s *PaymentResumeService) verifySignature(payload string, signature string) bool {
	if s == nil {
		return false
	}
	// A token can have been signed by any retained primary/legacy key. Try each
	// canonical signature until one matches, keeping current creation tied to the
	// primary key separately.
	for _, key := range s.verifyKeys {
		if hmac.Equal([]byte(signature), []byte(ResumeSignPaymentResumePayload(payload, key))) {
			return true
		}
	}
	return false
}

// ResumeValidatePaymentResumeExpiry rejects only positive expiration earlier
// than current Unix seconds, using the supplied reason/message. Nonpositive
// values and the exact current second are accepted; it adds no issued-time or
// maximum-lifetime rule.
func ResumeValidatePaymentResumeExpiry(expiresAt int64, code, message string) error {
	if expiresAt <= 0 {
		return nil
	}
	if time.Now().Unix() > expiresAt {
		return infraerrors.BadRequest(code, message)
	}
	return nil
}

// sign returns the encoded payload signature using the service's primary key.
func (s *PaymentResumeService) sign(payload string) string {
	return ResumeSignPaymentResumePayload(payload, s.signingKey)
}

// ResumeSignPaymentResumePayload returns unpadded base64url HMAC-SHA256 over
// the supplied encoded payload text. It accepts key bytes as given and
// discards hash Write errors; callers enforce configured-key readiness
// separately.
func ResumeSignPaymentResumePayload(payload string, key []byte) string {
	mac := hmac.New(sha256.New, key)
	// Hash the exact encoded payload spelling so signer and verifier agree on one
	// token representation. hash.Hash Write does not return an error here, so its
	// result is discarded.
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// ResolvePaymentResumeSigningKeys chooses a parsed explicit primary key or the
// supplied legacy key when explicit input is blank. A distinct nonempty legacy
// key becomes verification fallback only when an explicit primary exists.
//
// The returned legacy slices are shared with the caller, while explicit parsed
// bytes are newly allocated. NewPaymentResumeService later copies selected
// material before use. Equal legacy/primary bytes suppress the duplicate
// fallback; absent both returns nil values. This separates deployment key
// selection from the service's parsing-readiness gate and copied-key
// ownership.
func ResolvePaymentResumeSigningKeys(raw string, legacyKey []byte) ([]byte, [][]byte) {
	signingKey := ParsePaymentResumeSigningKey(raw)
	// Absent explicit signing text selects the supplied legacy bytes as primary
	// material. The returned slice stays caller-owned until
	// NewPaymentResumeService copies it for token operations.
	if len(signingKey) == 0 {
		if len(legacyKey) == 0 {
			return nil, nil
		}
		return legacyKey, nil
	}
	if len(legacyKey) == 0 || bytes.Equal(legacyKey, signingKey) {
		return signingKey, nil
	}
	return signingKey, [][]byte{legacyKey}
}

// ParsePaymentResumeSigningKey trims input and decodes valid even-length hex
// text of at least sixty-four characters. Blank input returns nil; invalid,
// odd or shorter nonblank text becomes raw UTF-8 bytes, so this helper imposes
// no universal byte-length requirement on a signing key.
func ParsePaymentResumeSigningKey(raw string) []byte {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	// Treat at least sixty-four even hex characters as optional binary key
	// notation. A failed decode still permits raw text so nonhex deployment
	// secrets remain usable as signing bytes.
	if len(raw) >= 64 && len(raw)%2 == 0 {
		if decoded, err := hex.DecodeString(raw); err == nil && len(decoded) > 0 {
			return decoded
		}
	}
	return []byte(raw)
}
