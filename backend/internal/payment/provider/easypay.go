package provider

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TokenFlux/TokenRouter/internal/payment"
)

// EasyPay hosted checkout signs a browser URL, while API checkout posts the
// same merchant identity and retrieves payment references. Callback processing
// validates one value per accepted field before checking its signature; a
// successful signature still leaves order association and amount confirmation
// to the payment core.
//
// Query responses support top-level and nested variants, with pointer presence
// deciding which representation wins even when the winning value is empty.
// Refund submission tries the merchant order label before the upstream trade
// label and advances only for an order-not-found diagnostic. HTTP status
// acceptance belongs to each response parser, so the creation wrapper can
// accept a successful JSON code from a non-success HTTP response.

// EasyPay protocol values select request modes, paid states, signing text and
// bounded HTTP reads or diagnostic summaries.
const (
	easypayCodeSuccess     = 1                // successful JSON response code
	easypayStatusPaid      = 1                // paid numeric query state
	easypayHTTPTimeout     = 10 * time.Second // default HTTP request timeout
	maxEasypayResponseSize = 1 << 20          // response prefix in bytes
	maxEasypayErrorSummary = 512              // diagnostic prefix in bytes
	tradeStatusSuccess     = "TRADE_SUCCESS"  // paid textual trade state
	signTypeMD5            = "MD5"            // outbound signing type label
	paymentModePopup       = "popup"          // exact hosted checkout selector
	deviceMobile           = "mobile"         // mobile request device label
)

// EasyPay sends signed checkout, query and refund requests for one configured
// aggregation instance.
type EasyPay struct {
	instanceID string // configured instance identity
	// config owns the constructor copy of string settings; payment operations
	// read it as immutable provider configuration.
	config map[string]string
	// httpClient is reused for requests; a nil client selects the default timeout
	// in postRaw.
	httpClient *http.Client
}

// easyPayCustomMethod maps a visible local method to one upstream form value.
type easyPayCustomMethod struct {
	Type         string `json:"type"`         // visible local method
	UpstreamType string `json:"upstreamType"` // upstream payment type
	DisplayName  string `json:"displayName"`  // configured display text
}

// easyPayQueryData holds optional nested query fields considered after their
// top-level counterparts.
type easyPayQueryData struct {
	// TradeStatus wins over numeric Status when top-level TradeStatus is absent,
	// including an unrecognized nested string.
	TradeStatus *string `json:"trade_status"`
	// Status supplies a nested numeric state only after both textual states and
	// top-level Status are absent.
	Status *int `json:"status"`
	// Money supplies amount text when top-level Money is absent; an empty
	// top-level string blocks this value.
	Money *string `json:"money"`
	// TradeNo replaces the requested reference only when top-level TradeNo is
	// absent and this nested string is nonempty.
	TradeNo *string `json:"trade_no"`
}

// easyPayQueryResponse represents both supported JSON layouts while retaining
// presence for precedence decisions.
type easyPayQueryResponse struct {
	Code int    `json:"code"` // unused query response code
	Msg  string `json:"msg"`  // unused query diagnostic text
	// TradeStatus has first status precedence whenever present; an unrecognized
	// or empty value keeps the result pending.
	TradeStatus *string `json:"trade_status"`
	// Status is considered after both textual states are absent, and presence
	// blocks nested numeric Status.
	Status *int `json:"status"`
	// Money has amount precedence whenever present, including empty text that
	// later parses as zero.
	Money *string `json:"money"`
	// TradeNo presence blocks nested TradeNo; a nonempty value replaces the
	// fallback reference supplied by QueryOrder.
	TradeNo *string          `json:"trade_no"`
	Data    easyPayQueryData `json:"data"` // nested query response fields
}

// easyPayRefundAttempt pairs a request form with the reference returned on its
// success.
type easyPayRefundAttempt struct {
	// params and refundID describe the same attempt: params contains either
	// merchant order or upstream trade identity, and refundID returns that
	// selected identity on success.
	params   map[string]string // individual refund request form
	refundID string            // selected refund reference label
}

// NewEasyPay validates five nonblank connection settings and copies their
// string map before normalizing apiBase. It installs a ten-second HTTP client;
// API operations report upstream credential or connectivity errors when
// requests run.
func NewEasyPay(instanceID string, config map[string]string) (*EasyPay, error) {
	for _, k := range []string{"pid", "pkey", "apiBase", "notifyUrl", "returnUrl"} {
		if strings.TrimSpace(config[k]) == "" {
			return nil, fmt.Errorf("easypay config missing required key: %s", k)
		}
	}
	cfg := make(map[string]string, len(config))
	maps.Copy(cfg, config)
	cfg["apiBase"] = normalizeEasyPayAPIBase(cfg["apiBase"])
	return &EasyPay{
		instanceID: instanceID,
		config:     cfg,
		httpClient: &http.Client{Timeout: easypayHTTPTimeout},
	}, nil
}

// normalizeEasyPayAPIBase removes recognized operation suffixes from a
// configured service base. A parsed URL with scheme and host loses its query,
// fragment and escaped path before suffix removal, so subsequent operations
// append their own endpoints.
//
// Unparseable or incomplete URLs instead pass through textual suffix trimming.
// This normalization accepts the supplied scheme and host, and callers needing
// destination restrictions apply those rules separately before provider
// construction.
func normalizeEasyPayAPIBase(apiBase string) string {
	base := strings.TrimSpace(apiBase)
	if base == "" {
		return ""
	}
	if parsed, err := url.Parse(base); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.RawQuery = ""
		parsed.Fragment = ""
		parsed.RawPath = ""
		parsed.Path = trimEasyPayEndpointPath(parsed.Path)
		return strings.TrimRight(parsed.String(), "/")
	}
	// Use textual suffix removal when URL components cannot be read; this keeps
	// configured prefixes available to endpoint construction.
	return strings.TrimRight(trimEasyPayEndpointPath(base), "/")
}

// trimEasyPayEndpointPath removes a trailing submit.php, mapi.php or api.php
// suffix case-insensitively after whitespace and slash trimming. Unrecognized
// paths remain available as prefixes for later endpoint construction.
func trimEasyPayEndpointPath(path string) string {
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	lower := strings.ToLower(path)
	for _, endpoint := range []string{"/submit.php", "/mapi.php", "/api.php"} {
		if strings.HasSuffix(lower, endpoint) {
			// Remove only the recognized operation suffix; a preceding installation
			// path remains part of each later endpoint URL.
			return strings.TrimRight(path[:len(path)-len(endpoint)], "/")
		}
	}
	return path
}

// apiBase returns the normalized configured endpoint prefix, or empty for a
// nil provider. It repeats normalization during reads so request construction
// uses the same suffix handling as NewEasyPay.
func (e *EasyPay) apiBase() string {
	if e == nil {
		return ""
	}
	return normalizeEasyPayAPIBase(e.config["apiBase"])
}

// Name returns the fixed display name for this aggregation adapter.
func (e *EasyPay) Name() string { return "EasyPay" }

// ProviderKey returns the registry key identifying EasyPay provider instances.
func (e *EasyPay) ProviderKey() string { return payment.TypeEasyPay }

// SupportedTypes returns a fresh list starting with Alipay and WeChat and
// appends parsed custom local methods. Duplicate custom labels remain in the
// list, while incomplete or malformed custom entries are omitted.
func (e *EasyPay) SupportedTypes() []payment.PaymentType {
	types := []payment.PaymentType{payment.TypeAlipay, payment.TypeWxpay}
	for _, method := range e.customMethods() {
		if method.Type != "" {
			types = append(types, method.Type)
		}
	}
	return types
}

// MerchantIdentityMetadata returns a fresh map containing the trimmed
// configured merchant pid. A nil provider or blank pid returns nil; payment
// confirmation consumes this identity independently of the visible payment
// method.
func (e *EasyPay) MerchantIdentityMetadata() map[string]string {
	if e == nil {
		return nil
	}
	pid := strings.TrimSpace(e.config["pid"])
	if pid == "" {
		return nil
	}
	return map[string]string{"pid": pid}
}

// CreatePayment selects hosted checkout only for the exact configured popup
// value and delegates every other value to API checkout. Hosted construction
// returns a signed URL immediately; API construction uses ctx for its HTTP
// request.
func (e *EasyPay) CreatePayment(ctx context.Context, req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	mode := e.config["paymentMode"]
	if mode == paymentModePopup {
		return e.createRedirectPayment(req)
	}
	// Use API checkout for every non-popup label, retaining creation error and
	// URL handling at that delegated request implementation.
	return e.createAPIPayment(ctx, req)
}

// createRedirectPayment signs a submit.php browser URL using the selected
// upstream method, merchant label, amount and callback destinations. Request
// destinations override nonempty configured destinations, and the response
// leaves TradeNo empty for later channel observations.
func (e *EasyPay) createRedirectPayment(req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	notifyURL, returnURL := e.resolveURLs(req)
	paymentType := e.upstreamPaymentType(req.PaymentType)
	params := map[string]string{
		"pid": e.config["pid"], "type": paymentType,
		"out_trade_no": req.OrderID, "notify_url": notifyURL,
		"return_url": returnURL, "name": req.Subject,
		"money": req.Amount,
	}
	if cid := e.resolveCID(paymentType); cid != "" {
		params["cid"] = cid
	}
	if req.IsMobile {
		params["device"] = deviceMobile
	}
	// Sign the merchant form before adding sign_type; that algorithm label
	// remains excluded from the shared wire signature.
	params["sign"] = easyPaySign(params, e.config["pkey"])
	params["sign_type"] = signTypeMD5

	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	payURL := e.apiBase() + "/submit.php?" + q.Encode()
	return &payment.CreatePaymentResponse{PayURL: payURL}, nil
}

// createAPIPayment posts a signed mapi.php form and returns upstream trade,
// launch and QR references. JSON code one is its acceptance condition because
// post discards HTTP status, while malformed JSON or another code returns an
// error.
//
// A mobile request prefers nonempty payurl2 over payurl. Slash-prefixed launch
// and QR references resolve against the instance base; other references retain
// their exact text so opaque QR payloads and application links remain usable
// by checkout consumers.
func (e *EasyPay) createAPIPayment(ctx context.Context, req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	notifyURL, returnURL := e.resolveURLs(req)
	paymentType := e.upstreamPaymentType(req.PaymentType)
	params := map[string]string{
		"pid": e.config["pid"], "type": paymentType,
		"out_trade_no": req.OrderID, "notify_url": notifyURL,
		"return_url": returnURL, "name": req.Subject,
		"money": req.Amount, "clientip": req.ClientIP,
	}
	if cid := e.resolveCID(paymentType); cid != "" {
		params["cid"] = cid
	}
	if req.IsMobile {
		params["device"] = deviceMobile
	}
	// Sign the merchant form before adding sign_type; that algorithm label
	// remains excluded from the shared wire signature.
	params["sign"] = easyPaySign(params, e.config["pkey"])
	params["sign_type"] = signTypeMD5

	body, err := e.post(ctx, e.apiBase()+"/mapi.php", params)
	if err != nil {
		return nil, fmt.Errorf("easypay create: %w", err)
	}
	var resp struct {
		Code    int    `json:"code"`     // creation acceptance response code
		Msg     string `json:"msg"`      // creation failure message text
		TradeNo string `json:"trade_no"` // returned upstream trade reference
		PayURL  string `json:"payurl"`   // primary payment launch reference
		// PayURL2 replaces PayURL when the request is mobile and this returned
		// alternative is nonempty.
		PayURL2 string `json:"payurl2"`
		QRCode  string `json:"qrcode"` // returned QR payment payload
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("easypay parse: %w", err)
	}
	if resp.Code != easypayCodeSuccess {
		return nil, fmt.Errorf("easypay error: %s", resp.Msg)
	}
	payURL := resp.PayURL
	// Prefer the mobile launch reference before resolving relative paths, so
	// either URL variant uses the same base normalization.
	if req.IsMobile && resp.PayURL2 != "" {
		payURL = resp.PayURL2
	}
	base := e.apiBase()
	return &payment.CreatePaymentResponse{
		TradeNo: resp.TradeNo,
		PayURL:  resolveEasyPayReturnedRef(base, payURL),
		QRCode:  resolveEasyPayReturnedRef(base, resp.QRCode),
	}, nil
}

// resolveEasyPayReturnedRef resolves slash-prefixed response references
// against a usable service base. It trims only for parsing and prefix
// detection, and returns the supplied text unchanged for opaque tokens,
// application links or unusable URLs.
//
// A root path inherits the base origin, whereas a protocol-relative reference
// can supply another host while inheriting the base scheme. Checkout callers
// therefore receive the upstream reference format rather than an enforced
// same-origin destination, and keep any destination policy at their own
// validation step.
func resolveEasyPayReturnedRef(apiBase, ref string) string {
	trimmed := strings.TrimSpace(ref)
	if !strings.HasPrefix(trimmed, "/") {
		return ref
	}
	base, err := url.Parse(strings.TrimSpace(apiBase))
	// Retain the upstream reference when the base lacks usable URL components,
	// avoiding construction of a misleading launch destination.
	if err != nil || base.Scheme == "" || base.Host == "" {
		return ref
	}
	parsed, err := url.Parse(trimmed)
	// Retain a reference that cannot participate in relative resolution, leaving
	// upstream payload interpretation to its checkout consumer.
	if err != nil || parsed.Scheme != "" {
		return ref
	}
	return base.ResolveReference(parsed).String()
}

// resolveURLs returns notification and return destinations, taking each
// nonempty request value before its configured value. The check uses literal
// emptiness, so whitespace supplied by a caller also overrides configuration.
func (e *EasyPay) resolveURLs(req payment.CreatePaymentRequest) (string, string) {
	notifyURL := req.NotifyURL
	if notifyURL == "" {
		notifyURL = e.config["notifyUrl"]
	}
	returnURL := req.ReturnURL
	if returnURL == "" {
		returnURL = e.config["returnUrl"]
	}
	return notifyURL, returnURL
}

// customMethods decodes custom mappings into a fresh slice and trims each
// local code, upstream code and display label. Missing or malformed
// configuration returns nil; entries missing either code are skipped without
// validating the remaining codes here.
func (e *EasyPay) customMethods() []easyPayCustomMethod {
	if e == nil {
		return nil
	}
	raw := strings.TrimSpace(e.config["customMethods"])
	if raw == "" {
		return nil
	}
	var methods []easyPayCustomMethod
	// Discard malformed custom configuration as an empty mapping list, so
	// built-in advertised methods remain available to registry consumers.
	if err := json.Unmarshal([]byte(raw), &methods); err != nil {
		return nil
	}
	result := make([]easyPayCustomMethod, 0, len(methods))
	for _, method := range methods {
		method.Type = strings.TrimSpace(method.Type)
		method.UpstreamType = strings.TrimSpace(method.UpstreamType)
		method.DisplayName = strings.TrimSpace(method.DisplayName)
		// Skip incomplete mappings before advertisement and request translation; a
		// visible code needs a corresponding upstream form value here.
		if method.Type == "" || method.UpstreamType == "" {
			continue
		}
		result = append(result, method)
	}
	return result
}

// upstreamPaymentType returns the first custom mapping matching the trimmed
// local method. When no mapping matches it returns that trimmed method
// directly, allowing built-in labels to become upstream form values.
func (e *EasyPay) upstreamPaymentType(paymentType string) string {
	paymentType = strings.TrimSpace(paymentType)
	for _, method := range e.customMethods() {
		if paymentType == method.Type {
			return method.UpstreamType
		}
	}
	// Unmatched visible labels pass directly to the upstream form, so the two
	// built-in methods need no custom mapping entry.
	return paymentType
}

// QueryOrder posts an order lookup using tradeNo as the merchant out_trade_no
// label and ctx for HTTP cancellation. Its parser checks HTTP status and
// retains the requested label when the selected response reference is empty.
func (e *EasyPay) QueryOrder(ctx context.Context, tradeNo string) (*payment.QueryOrderResponse, error) {
	params := map[string]string{
		"act": "order", "pid": e.config["pid"],
		"key": e.config["pkey"], "out_trade_no": tradeNo,
	}
	body, statusCode, err := e.postRaw(ctx, e.apiBase()+"/api.php", params)
	if err != nil {
		return nil, fmt.Errorf("easypay query: %w", err)
	}
	return parseEasyPayQueryResponse(statusCode, body, tradeNo, e.MerchantIdentityMetadata())
}

// parseEasyPayQueryResponse accepts successful HTTP JSON objects and selects
// status, money and trade identity from optional top-level or nested fields.
// Status precedence is top-level textual, nested textual, top-level numeric,
// then nested numeric; a present unrecognized value prevents later
// alternatives.
//
// Money and trade references prefer top-level presence separately. Empty
// selected money or invalid syntax produces zero, while range overflow can
// produce infinity despite a discarded parse error. Empty selected trade text
// keeps fallbackTradeNo. The supplied metadata map is returned directly, and
// Code or Msg does not determine query acceptance; the payment core confirms
// any reported paid amount afterward.
func parseEasyPayQueryResponse(statusCode int, body []byte, fallbackTradeNo string, metadata map[string]string) (*payment.QueryOrderResponse, error) {
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("easypay query HTTP %d", statusCode)
	}

	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil, fmt.Errorf("easypay query empty response (HTTP %d)", statusCode)
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "<!doctype html") || strings.HasPrefix(lower, "<html") ||
		(strings.HasPrefix(lower, "<") && strings.Contains(lower, "html")) {
		return nil, fmt.Errorf("easypay query non-JSON response (HTTP %d)", statusCode)
	}
	if !strings.HasPrefix(trimmed, "{") {
		return nil, fmt.Errorf("easypay query non-JSON response (HTTP %d)", statusCode)
	}

	var resp easyPayQueryResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("easypay query non-JSON response (HTTP %d)", statusCode)
	}
	// Choose by field presence in descending precedence; a present unknown state
	// deliberately prevents another response layout from claiming paid.
	status := payment.ProviderStatusPending
	if resp.TradeStatus != nil {
		if *resp.TradeStatus == tradeStatusSuccess {
			status = payment.ProviderStatusPaid
		}
	} else if resp.Data.TradeStatus != nil {
		if *resp.Data.TradeStatus == tradeStatusSuccess {
			status = payment.ProviderStatusPaid
		}
	} else if resp.Status != nil {
		if *resp.Status == easypayStatusPaid {
			status = payment.ProviderStatusPaid
		}
	} else if resp.Data.Status != nil && *resp.Data.Status == easypayStatusPaid {
		status = payment.ProviderStatusPaid
	}

	// Select money and trade references independently of status; a present empty
	// top-level value blocks its nested alternative.
	money := ""
	if resp.Money != nil {
		money = *resp.Money
	} else if resp.Data.Money != nil {
		money = *resp.Data.Money
	}
	responseTradeNo := fallbackTradeNo
	if resp.TradeNo != nil {
		if *resp.TradeNo != "" {
			responseTradeNo = *resp.TradeNo
		}
	} else if resp.Data.TradeNo != nil && *resp.Data.TradeNo != "" {
		responseTradeNo = *resp.Data.TradeNo
	}

	// Discard the numeric parse error here; syntax failures yield zero and
	// overflow can yield infinity, both reaching core amount confirmation.
	amount, _ := strconv.ParseFloat(money, 64)
	return &payment.QueryOrderResponse{
		TradeNo:  responseTradeNo,
		Status:   status,
		Amount:   amount,
		Metadata: metadata,
	}, nil
}

// VerifyNotification validates EasyPay form structure, merchant identity,
// positive finite money and the computed signature before returning a callback
// observation. It accepts a fixed key set with exactly one value per key and
// requires delimiter-free structured fields, while name and param retain free
// text.
//
// Signing sorts nonempty parameters and excludes sign and sign_type. Signature
// text must equal the computed lowercase MD5 hex exactly; sign_type is
// accepted but does not select another algorithm. The adapter adds no
// timestamp window, and the payment core subsequently associates the merchant
// order label and validates the amount against local state.
// @project-doc docs/domains/payments_and_entitlements.md#callback_security
func (e *EasyPay) VerifyNotification(_ context.Context, rawBody string, _ map[string]string) (*payment.PaymentNotification, error) {
	values, err := url.ParseQuery(rawBody)
	if err != nil {
		return nil, fmt.Errorf("parse notify: %w", err)
	}
	// Build one accepted value per key before signing; otherwise parsed field
	// selection and signature input could describe different callback data.
	params := make(map[string]string, len(values))
	for key, entries := range values {
		switch key {
		case "pid", "trade_no", "out_trade_no", "type", "name", "money", "trade_status", "param", "sign", "sign_type":
		default:
			return nil, fmt.Errorf("unexpected notify param: %s", key)
		}
		// Reject duplicate values before extracting the first entry, keeping the
		// signed form identical to the callback fields later consumed.
		if len(entries) != 1 {
			return nil, fmt.Errorf("duplicate notify param: %s", key)
		}
		params[key] = entries[0]
	}
	// Constrain identifiers, type, money and state before raw pair concatenation;
	// name and param remain free text in this wire format.
	for _, key := range []string{"pid", "trade_no", "out_trade_no", "type", "money", "trade_status"} {
		value := params[key]
		if value == "" {
			return nil, fmt.Errorf("missing notify param: %s", key)
		}
		if strings.ContainsAny(value, "&=\x00\r\n") || strings.TrimSpace(value) != value {
			return nil, fmt.Errorf("invalid notify param: %s", key)
		}
	}
	if !payment.ConfigEasyPayCustomMethodCodePattern.MatchString(params["type"]) {
		return nil, fmt.Errorf("invalid notify param: type")
	}
	if params["pid"] != strings.TrimSpace(e.config["pid"]) {
		return nil, fmt.Errorf("easypay notify pid mismatch")
	}
	amount, err := strconv.ParseFloat(params["money"], 64)
	if err != nil || !payment.IsValidProviderAmount(amount) {
		return nil, fmt.Errorf("invalid notify param: money")
	}
	sign := params["sign"]
	if sign == "" {
		return nil, fmt.Errorf("missing sign")
	}
	if !easyPayVerifySign(params, e.config["pkey"], sign) {
		return nil, fmt.Errorf("invalid signature")
	}
	status := payment.ProviderStatusFailed
	if params["trade_status"] == tradeStatusSuccess {
		status = payment.ProviderStatusSuccess
	}
	return &payment.PaymentNotification{
		TradeNo: params["trade_no"], OrderID: params["out_trade_no"],
		Amount: amount, Status: status, RawData: rawBody, Metadata: e.MerchantIdentityMetadata(),
	}, nil
}

// Refund submits available merchant-order and upstream-trade forms in that
// order, returning the identity of the successful attempt. A transport error
// stops submission immediately; a parsed order-not-found diagnostic permits
// the next form, while other parsed failures return at once.
//
// Each form carries the requested amount but omits the workflow OperationID.
// The alternative is another order lookup representation, so callers should
// not infer request-level deduplication from the returned RefundID. A
// successful parser produces success status, and an exhausted attempt sequence
// returns its first parsed error.
func (e *EasyPay) Refund(ctx context.Context, req payment.RefundRequest) (*payment.RefundResponse, error) {
	attempts := e.refundAttempts(req)
	if len(attempts) == 0 {
		return nil, fmt.Errorf("easypay refund missing order identifier")
	}
	var firstErr error
	for i, attempt := range attempts {
		body, status, err := e.postRaw(ctx, e.apiBase()+"/api.php?act=refund", attempt.params)
		if err != nil {
			return nil, fmt.Errorf("easypay refund request: %w", err)
		}
		if err := parseEasyPayRefundResponse(status, body); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			// A missing-order diagnostic permits the other identifier representation;
			// transport errors and other channel rejections do not reach this retry.
			if i+1 < len(attempts) && isEasyPayRefundOrderNotFound(err) {
				continue
			}
			return nil, err
		}
		return &payment.RefundResponse{RefundID: attempt.refundID, Status: payment.ProviderStatusSuccess}, nil
	}
	return nil, firstErr
}

// refundAttempts builds independent request maps for nonblank merchant order
// and upstream trade references, in that order. Each returned refundID is the
// trimmed identifier used by its form, including when both labels happen to
// have equal text.
//
// The shared merchant credentials and amount are copied into each map before
// adding its identity field. This keeps the alternative forms separate for
// Refund while leaving upstream repeat-submission behavior to the service;
// local OperationID and refund request identity are not added by this adapter.
func (e *EasyPay) refundAttempts(req payment.RefundRequest) []easyPayRefundAttempt {
	base := map[string]string{
		"pid": e.config["pid"], "key": e.config["pkey"], "money": req.Amount,
	}
	var attempts []easyPayRefundAttempt
	if orderID := strings.TrimSpace(req.OrderID); orderID != "" {
		params := cloneStringMap(base)
		params["out_trade_no"] = orderID
		attempts = append(attempts, easyPayRefundAttempt{params: params, refundID: orderID})
	}
	if tradeNo := strings.TrimSpace(req.TradeNo); tradeNo != "" {
		params := cloneStringMap(base)
		params["trade_no"] = tradeNo
		attempts = append(attempts, easyPayRefundAttempt{params: params, refundID: tradeNo})
	}
	return attempts
}

// cloneStringMap returns a fresh map containing the supplied string entries.
func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

// isEasyPayRefundOrderNotFound reports whether an error contains one of the
// accepted Chinese or case-insensitive English missing-order phrases. Refund
// uses this diagnostic classification to permit its alternative form; nil
// returns false.
func isEasyPayRefundOrderNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	return strings.Contains(msg, "订单编号不存在") ||
		strings.Contains(msg, "订单不存在") ||
		strings.Contains(lower, "order not found") ||
		strings.Contains(lower, "not exist")
}

// parseEasyPayRefundResponse requires successful HTTP JSON containing a
// response code recognized as success. Failures use a bounded body summary
// unless a nonblank JSON message exists, in which case that trimmed message is
// returned at its supplied length.
func parseEasyPayRefundResponse(status int, body []byte) error {
	summary := summarizeEasyPayResponse(body)
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return fmt.Errorf("easypay refund HTTP %d: %s", status, summary)
	}

	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return fmt.Errorf("easypay refund empty response (HTTP %d): %s", status, summary)
	}

	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "<!doctype html") || strings.HasPrefix(lower, "<html") ||
		(strings.HasPrefix(lower, "<") && strings.Contains(lower, "html")) {
		return fmt.Errorf("easypay refund non-JSON response (HTTP %d): %s", status, summary)
	}

	var resp struct {
		Code any `json:"code"` // numeric or textual response code
		// Msg replaces the bounded body summary whenever its trimmed text is
		// nonempty, so supplied JSON diagnostics retain their length.
		Msg string `json:"msg"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("easypay refund non-JSON response (HTTP %d): %s", status, summary)
	}
	if !easyPayResponseCodeIsSuccess(resp.Code) {
		msg := strings.TrimSpace(resp.Msg)
		if msg == "" {
			// Use the bounded body diagnostic when JSON contains no usable message,
			// retaining context for refund failure classification.
			msg = summary
		}
		return fmt.Errorf("easypay refund failed (HTTP %d): %s", status, msg)
	}
	return nil
}

// easyPayResponseCodeIsSuccess accepts a decoded float whose integer
// conversion equals one or trimmed integer text equal to one. Other types
// fail, and fractional float codes can pass because conversion truncates them.
func easyPayResponseCodeIsSuccess(code any) bool {
	switch v := code.(type) {
	case float64:
		// Integer conversion truncates fractional numeric response codes before
		// comparison.
		return int(v) == easypayCodeSuccess
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		return err == nil && n == easypayCodeSuccess
	default:
		return false
	}
}

// summarizeEasyPayResponse collapses body whitespace and uses an empty marker
// for absent text. Long summaries keep a 512-byte prefix shortened to valid
// UTF-8, then add an ellipsis; the text is not credential-filtered.
func summarizeEasyPayResponse(body []byte) string {
	summary := strings.Join(strings.Fields(string(body)), " ")
	if summary == "" {
		return "<empty>"
	}
	if len(summary) > maxEasypayErrorSummary {
		// Cap the collapsed diagnostic by bytes, then shorten an incomplete UTF-8
		// prefix before returning it with the ellipsis.
		truncated := summary[:maxEasypayErrorSummary]
		for truncated != "" && !utf8.ValidString(truncated) {
			truncated = truncated[:len(truncated)-1]
		}
		return truncated + "..."
	}
	return summary
}

// resolveCID prefers cidAlipay for upstream types beginning with alipay and
// cidWxpay for every other type. Each branch uses generic cid when its
// selected override is empty, retaining the configured string unchanged.
func (e *EasyPay) resolveCID(paymentType string) string {
	if strings.HasPrefix(paymentType, "alipay") {
		if v := e.config["cidAlipay"]; v != "" {
			return v
		}
		// Use the shared channel ID when the Alipay-specific override is empty,
		// maintaining a destination for that upstream method family.
		return e.config["cid"]
	}
	if v := e.config["cidWxpay"]; v != "" {
		return v
	}
	// Use the shared channel ID when the non-Alipay override is empty; custom
	// upstream methods follow this same branch.
	return e.config["cid"]
}

// post returns the body and transport or read error from postRaw while
// discarding HTTP status. createAPIPayment consequently determines acceptance
// from its JSON parser, including bodies returned with an HTTP error status.
func (e *EasyPay) post(ctx context.Context, endpoint string, params map[string]string) ([]byte, error) {
	body, _, err := e.postRaw(ctx, endpoint, params)
	return body, err
}

// postRaw sends a form request using ctx, the configured client or a
// ten-second default, and closes the response body. Reads stop at one MiB,
// returning that prefix and HTTP status for operation-specific parsing; an
// oversized body is not independently rejected.
func (e *EasyPay) postRaw(ctx context.Context, endpoint string, params map[string]string) ([]byte, int, error) {
	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := e.httpClient
	if client == nil {
		// A nil injected client still receives the constructor timeout policy, so
		// direct provider fixtures retain bounded HTTP request duration.
		client = &http.Client{Timeout: easypayHTTPTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	// Close the response after reading its prefix; close errors are discarded
	// because parsing uses the obtained body and read error.
	defer func() { _ = resp.Body.Close() }()
	// Read at most one MiB for operation parsers; a larger response becomes a
	// prefix rather than an explicit oversize failure.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxEasypayResponseSize))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// easyPaySign produces the lowercase MD5 hex of sorted, nonempty form pairs
// followed by the merchant secret. sign and sign_type are excluded, and
// retained values are joined as raw key=value text rather than URL-escaped
// form text.
//
// The callback validator constrains structured values before this shared
// signing step, while creation supplies its own request values. Consequently
// free text and delimiter handling belong to the callers using this wire
// format, and changing the sort or escaping policy would change verification
// of the same parameters.
func easyPaySign(params map[string]string, pkey string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || k == "sign_type" || v == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf strings.Builder
	// Builder writes cannot fail for their accepted inputs, so discarded write
	// results leave sorted raw pairs followed by the secret.
	for i, k := range keys {
		if i > 0 {
			_ = buf.WriteByte('&')
		}
		_, _ = buf.WriteString(k + "=" + params[k])
	}
	_, _ = buf.WriteString(pkey)
	hash := md5.Sum([]byte(buf.String()))
	return hex.EncodeToString(hash[:])
}

// easyPayVerifySign compares computed and supplied signature text using
// constant-time byte comparison. It performs no trimming or case
// normalization, so callers supply the exact lowercase hexadecimal text
// emitted by easyPaySign.
func easyPayVerifySign(params map[string]string, pkey string, sign string) bool {
	return hmac.Equal([]byte(easyPaySign(params, pkey)), []byte(sign))
}
