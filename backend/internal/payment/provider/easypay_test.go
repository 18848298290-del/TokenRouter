package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/TokenFlux/TokenRouter/internal/payment"
)

// TestEasyPayNotificationRejectsCheckoutParameters checks that checkout fields cannot authenticate as a notification.
func TestEasyPayNotificationRejectsCheckoutParameters(t *testing.T) {
	t.Parallel()

	e := &EasyPay{config: map[string]string{
		"pid": "1000", "pkey": "test-merchant-secret",
		"apiBase": "https://pay.example.com", "paymentMode": "popup",
		"notifyUrl": "https://site.example.com/api/v1/payment/webhook/easypay",
	}}
	created, err := e.CreatePayment(context.Background(), payment.CreatePaymentRequest{
		OrderID: "ORDER123", PaymentType: payment.TypeAlipay,
		Subject: "balance recharge", Amount: "650.00",
		ReturnURL: "https://site.example.com/payment/result",
	})
	if err != nil {
		t.Fatal(err)
	}
	payURL, err := url.Parse(created.PayURL)
	if err != nil {
		t.Fatal(err)
	}
	notification, err := e.VerifyNotification(context.Background(), payURL.RawQuery, nil)
	if err == nil || notification != nil {
		t.Fatalf("checkout parameters accepted: notification=%+v, err=%v", notification, err)
	}
}

// TestEasyPayNotificationParameters covers valid, extra, and duplicate notification fields.
func TestEasyPayNotificationParameters(t *testing.T) {
	t.Parallel()

	e := &EasyPay{config: map[string]string{"pid": "1000", "pkey": "test-merchant-secret"}}
	tests := []struct {
		name      string
		payType   string
		extraKey  string
		extra     string
		duplicate string
		wantError bool
	}{
		{name: "standard notification"},
		{name: "wechat notification", payType: "wxpay"},
		{name: "custom payment type", payType: "usdt_trc20"},
		{name: "optional param", extraKey: "param", extra: "merchant=value&extra=中文"},
		{name: "empty optional param", extraKey: "param"},
		{name: "return URL", extraKey: "return_url", extra: "https://site.example.com/payment/result", wantError: true},
		{name: "notify URL", extraKey: "notify_url", extra: "https://site.example.com/notify", wantError: true},
		{name: "device", extraKey: "device", extra: "mobile", wantError: true},
		{name: "unknown field", extraKey: "unknown", extra: "value", wantError: true},
		{name: "empty unknown field", extraKey: "unknown", wantError: true},
		{name: "empty return URL", extraKey: "return_url", wantError: true},
		{name: "duplicate status", duplicate: "trade_status", wantError: true},
		{name: "duplicate order", duplicate: "out_trade_no", wantError: true},
		{name: "duplicate signature", duplicate: "sign", wantError: true},
		{name: "duplicate amount", duplicate: "money", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			params := map[string]string{
				"pid": "1000", "trade_no": "UPSTREAM123", "out_trade_no": "ORDER123",
				"type": "alipay", "name": "充值 & 套餐=10%", "money": "650.00",
				"trade_status": tradeStatusSuccess,
			}
			if tt.payType != "" {
				params["type"] = tt.payType
			}
			if tt.extraKey != "" {
				params[tt.extraKey] = tt.extra
			}
			params["sign"] = easyPaySign(params, e.config["pkey"])
			params["sign_type"] = signTypeMD5
			values := url.Values{}
			for key, value := range params {
				values.Set(key, value)
			}
			if tt.duplicate != "" {
				values.Add(tt.duplicate, "different-value")
			}
			raw := values.Encode()
			notification, err := e.VerifyNotification(context.Background(), raw, nil)
			if tt.wantError {
				if err == nil || notification != nil {
					t.Fatalf("invalid parameters accepted: notification=%+v, err=%v", notification, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if notification.Status != payment.ProviderStatusSuccess || notification.Amount != 650 ||
				notification.OrderID != "ORDER123" || notification.TradeNo != "UPSTREAM123" ||
				notification.Metadata["pid"] != "1000" || notification.RawData != raw {
				t.Fatalf("unexpected notification: %+v", notification)
			}
			// Changing the amount after signing invalidates the notification.
			values.Set("money", "651.00")
			if _, err := e.VerifyNotification(context.Background(), values.Encode(), nil); err == nil || !strings.Contains(err.Error(), "invalid signature") {
				t.Fatalf("tampered notification: %v", err)
			}
		})
	}
}

// TestEasyPayNotificationRejectsInvalidFields checks rejection of invalid fields before signature verification.
func TestEasyPayNotificationRejectsInvalidFields(t *testing.T) {
	t.Parallel()

	e := &EasyPay{config: map[string]string{"pid": "1000", "pkey": "test-merchant-secret"}}
	for _, key := range []string{"pid", "trade_no", "out_trade_no", "type", "money", "trade_status"} {
		for _, value := range []string{"", "demo&label", "demo=label", "demo\x00label", "demo\rlabel", "demo\nlabel", " demo", "demo "} {
			t.Run(key+"/"+value, func(t *testing.T) {
				t.Parallel()
				values := easyPayTestNotificationValues()
				values.Set(key, value)
				want := "invalid notify param: " + key
				if value == "" {
					want = "missing notify param: " + key
				}
				notification, err := e.VerifyNotification(context.Background(), values.Encode(), nil)
				if err == nil || err.Error() != want || notification != nil {
					t.Fatalf("field validation: notification=%+v, err=%v, want %q", notification, err, want)
				}
			})
		}
	}
	for _, tt := range []struct {
		key   string
		value string
		want  string
	}{
		{key: "pid", value: "2000", want: "easypay notify pid mismatch"},
		{key: "type", value: "alipay/demo", want: "invalid notify param: type"},
		{key: "type", value: "alipay@demo", want: "invalid notify param: type"},
		{key: "type", value: "alipay%26demo", want: "invalid notify param: type"},
		{key: "money", value: "NaN", want: "invalid notify param: money"},
		{key: "money", value: "+Inf", want: "invalid notify param: money"},
		{key: "money", value: "0", want: "invalid notify param: money"},
		{key: "money", value: "-1", want: "invalid notify param: money"},
		{key: "money", value: "invalid", want: "invalid notify param: money"},
	} {
		t.Run(tt.key+"/"+tt.value, func(t *testing.T) {
			t.Parallel()
			values := easyPayTestNotificationValues()
			values.Set(tt.key, tt.value)
			notification, err := e.VerifyNotification(context.Background(), values.Encode(), nil)
			if err == nil || err.Error() != tt.want || notification != nil {
				t.Fatalf("field validation: notification=%+v, err=%v, want %q", notification, err, tt.want)
			}
		})
	}
}

func TestEasyPayQueryOrderStatusMapping(t *testing.T) {
	t.Parallel()

	const orderID = "order-123"
	tests := []struct {
		name        string
		body        string
		wantStatus  string
		wantTradeNo string
		wantAmount  float64
	}{
		{
			name:        "top level trade success is paid",
			body:        `{"code":1,"trade_status":"TRADE_SUCCESS","status":0,"money":"12.34","trade_no":"gateway-123"}`,
			wantStatus:  payment.ProviderStatusPaid,
			wantTradeNo: "gateway-123",
			wantAmount:  12.34,
		},
		{
			name:        "waiting trade status with paid numeric status stays pending",
			body:        `{"code":1,"trade_status":"WAITING","status":1,"money":"12.34","trade_no":"gateway-123"}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: "gateway-123",
			wantAmount:  12.34,
		},
		{
			name:        "empty trade status with paid numeric status stays pending",
			body:        `{"code":1,"trade_status":"","status":1,"money":"12.34"}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: orderID,
			wantAmount:  12.34,
		},
		{
			name:        "nested data trade success is paid",
			body:        `{"code":1,"data":{"trade_status":"TRADE_SUCCESS","status":0,"money":"9.99","trade_no":"data-456"}}`,
			wantStatus:  payment.ProviderStatusPaid,
			wantTradeNo: "data-456",
			wantAmount:  9.99,
		},
		{
			name:        "legacy numeric paid status remains compatible",
			body:        `{"code":1,"status":1,"money":"3.21"}`,
			wantStatus:  payment.ProviderStatusPaid,
			wantTradeNo: orderID,
			wantAmount:  3.21,
		},
		{
			name:        "legacy numeric non paid status is pending",
			body:        `{"code":1,"status":0,"money":"3.21"}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: orderID,
			wantAmount:  3.21,
		},
		{
			name:        "query failure with missing status is pending",
			body:        `{"code":0,"msg":"订单不存在"}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: orderID,
		},
		{
			name:        "missing fields are pending",
			body:        `{}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: orderID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotForm url.Values
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("method = %q, want %q", r.Method, http.MethodPost)
				}
				if r.URL.Path != "/api.php" {
					t.Errorf("path = %q, want /api.php", r.URL.Path)
				}
				if err := r.ParseForm(); err != nil {
					t.Errorf("ParseForm: %v", err)
				}
				gotForm = make(url.Values, len(r.PostForm))
				for key, values := range r.PostForm {
					gotForm[key] = append([]string(nil), values...)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			provider := newTestEasyPay(t, server.URL)
			resp, err := provider.QueryOrder(context.Background(), orderID)
			if err != nil {
				t.Fatalf("QueryOrder returned error: %v", err)
			}
			if resp.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q (response=%+v)", resp.Status, tt.wantStatus, resp)
			}
			if resp.TradeNo != tt.wantTradeNo {
				t.Fatalf("trade_no = %q, want %q", resp.TradeNo, tt.wantTradeNo)
			}
			if resp.Amount != tt.wantAmount {
				t.Fatalf("amount = %v, want %v", resp.Amount, tt.wantAmount)
			}
			for key, want := range map[string]string{
				"act":          "order",
				"pid":          "pid-1",
				"key":          "pkey-1",
				"out_trade_no": orderID,
			} {
				if got := gotForm.Get(key); got != want {
					t.Fatalf("form[%s] = %q, want %q (form=%v)", key, got, want, gotForm)
				}
			}
		})
	}
}

func TestEasyPayQueryOrderRejectsUnsafeResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErr    string
	}{
		{
			name:       "non success status",
			statusCode: http.StatusBadGateway,
			body:       `{"code":0,"msg":"gateway error"}`,
			wantErr:    "easypay query HTTP 502",
		},
		{
			name:       "html response",
			statusCode: http.StatusOK,
			body:       "<html>secret-response</html>",
			wantErr:    "easypay query non-JSON response (HTTP 200)",
		},
		{
			name:       "plain response",
			statusCode: http.StatusOK,
			body:       "gateway unavailable",
			wantErr:    "easypay query non-JSON response (HTTP 200)",
		},
		{
			name:       "empty response",
			statusCode: http.StatusOK,
			body:       "",
			wantErr:    "easypay query empty response (HTTP 200)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			provider := newTestEasyPay(t, server.URL)
			_, err := provider.QueryOrder(context.Background(), "order-unsafe")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("QueryOrder error = %v, want containing %q", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), "secret-response") {
				t.Fatalf("QueryOrder error leaked response body: %v", err)
			}
		})
	}
}

func TestNormalizeEasyPayAPIBase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{input: "https://zpayz.cn", want: "https://zpayz.cn"},
		{input: "https://zpayz.cn/", want: "https://zpayz.cn"},
		{input: "https://zpayz.cn/mapi.php", want: "https://zpayz.cn"},
		{input: "https://zpayz.cn/submit.php", want: "https://zpayz.cn"},
		{input: "https://zpayz.cn/api.php", want: "https://zpayz.cn"},
		{input: "https://zpayz.cn/api.php?act=refund", want: "https://zpayz.cn"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			if got := normalizeEasyPayAPIBase(tt.input); got != tt.want {
				t.Fatalf("normalizeEasyPayAPIBase(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestEasyPayRefundNormalizesAPIBaseAndSendsOutTradeNoOnly(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotQuery url.Values
	var gotForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":1,"msg":"ok"}`))
	}))
	defer server.Close()

	provider := newTestEasyPay(t, server.URL+"/mapi.php")
	resp, err := provider.Refund(context.Background(), payment.RefundRequest{
		TradeNo: "trade-123",
		OrderID: "out-456",
		Amount:  "1.50",
	})
	if err != nil {
		t.Fatalf("Refund returned error: %v", err)
	}
	if resp == nil || resp.Status != payment.ProviderStatusSuccess {
		t.Fatalf("Refund response = %+v, want success", resp)
	}
	if gotPath != "/api.php" {
		t.Fatalf("refund path = %q, want /api.php", gotPath)
	}
	if gotQuery.Get("act") != "refund" {
		t.Fatalf("refund act query = %q, want refund", gotQuery.Get("act"))
	}
	for key, want := range map[string]string{
		"pid":          "pid-1",
		"key":          "pkey-1",
		"out_trade_no": "out-456",
		"money":        "1.50",
	} {
		if got := gotForm.Get(key); got != want {
			t.Fatalf("form[%s] = %q, want %q (form=%v)", key, got, want, gotForm)
		}
	}
	if got := gotForm.Get("trade_no"); got != "" {
		t.Fatalf("form[trade_no] = %q, want empty (form=%v)", got, gotForm)
	}
}

func TestEasyPayRefundRetriesWithTradeNoWhenOutTradeNoNotFound(t *testing.T) {
	t.Parallel()

	var gotForms []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.php" {
			t.Errorf("refund path = %q, want /api.php", r.URL.Path)
		}
		if r.URL.Query().Get("act") != "refund" {
			t.Errorf("refund act query = %q, want refund", r.URL.Query().Get("act"))
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		gotForms = append(gotForms, r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		if len(gotForms) == 1 {
			_, _ = w.Write([]byte(`{"code":0,"msg":"订单编号不存在！"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":1,"msg":"ok"}`))
	}))
	defer server.Close()

	provider := newTestEasyPay(t, server.URL+"/mapi.php")
	resp, err := provider.Refund(context.Background(), payment.RefundRequest{
		TradeNo: "trade-123",
		OrderID: "out-456",
		Amount:  "1.50",
	})
	if err != nil {
		t.Fatalf("Refund returned error: %v", err)
	}
	if resp == nil || resp.Status != payment.ProviderStatusSuccess || resp.RefundID != "trade-123" {
		t.Fatalf("Refund response = %+v, want success with trade refund id", resp)
	}
	if len(gotForms) != 2 {
		t.Fatalf("refund attempts = %d, want 2", len(gotForms))
	}
	if got := gotForms[0].Get("out_trade_no"); got != "out-456" {
		t.Fatalf("first form[out_trade_no] = %q, want out-456 (form=%v)", got, gotForms[0])
	}
	if got := gotForms[0].Get("trade_no"); got != "" {
		t.Fatalf("first form[trade_no] = %q, want empty (form=%v)", got, gotForms[0])
	}
	if got := gotForms[1].Get("trade_no"); got != "trade-123" {
		t.Fatalf("second form[trade_no] = %q, want trade-123 (form=%v)", got, gotForms[1])
	}
	if got := gotForms[1].Get("out_trade_no"); got != "" {
		t.Fatalf("second form[out_trade_no] = %q, want empty (form=%v)", got, gotForms[1])
	}
}

func TestEasyPayRefundResponseErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		body       string
		want       string
	}{
		{name: "html response", statusCode: http.StatusOK, body: "<html>bad config</html>", want: "non-JSON response (HTTP 200): <html>bad config</html>"},
		{name: "non json response", statusCode: http.StatusOK, body: "not json", want: "non-JSON response (HTTP 200): not json"},
		{name: "non 2xx response", statusCode: http.StatusBadGateway, body: "bad gateway", want: "HTTP 502: bad gateway"},
		{name: "empty response", statusCode: http.StatusOK, body: "", want: "empty response (HTTP 200): <empty>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			provider := newTestEasyPay(t, server.URL)
			_, err := provider.Refund(context.Background(), payment.RefundRequest{
				OrderID: "out-456",
				Amount:  "1.50",
			})
			if err == nil {
				t.Fatal("Refund returned nil error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Refund error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestSummarizeEasyPayResponsePreservesUTF8(t *testing.T) {
	t.Parallel()

	summary := summarizeEasyPayResponse([]byte(strings.Repeat("错", 171)))
	if !utf8.ValidString(summary) {
		t.Fatalf("summarizeEasyPayResponse returned invalid UTF-8: %q", summary)
	}
	if !strings.HasSuffix(summary, "...") {
		t.Fatalf("summarizeEasyPayResponse() = %q, want truncated suffix", summary)
	}
}

func TestEasyPayCustomMethodsUseConfiguredUpstreamType(t *testing.T) {
	t.Parallel()

	provider, err := NewEasyPay("test-instance", map[string]string{
		"pid":           "pid-1",
		"pkey":          "pkey-1",
		"apiBase":       "https://pay.example.com",
		"notifyUrl":     "https://example.com/notify",
		"returnUrl":     "https://example.com/return",
		"paymentMode":   paymentModePopup,
		"customMethods": `[{"type":"ldc","upstreamType":"epay","displayName":"LDC"},{"type":"usdt_trc20","upstreamType":"usdt","displayName":"USDT-TRC20"}]`,
	})
	if err != nil {
		t.Fatalf("NewEasyPay: %v", err)
	}

	resp, err := provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{
		OrderID:     "sub2-custom-1",
		Amount:      "1.00",
		PaymentType: "usdt_trc20",
		Subject:     "Custom EasyPay",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	payURL, err := url.Parse(resp.PayURL)
	if err != nil {
		t.Fatalf("parse pay url: %v", err)
	}
	if got := payURL.Query().Get("type"); got != "usdt" {
		t.Fatalf("pay url type = %q, want usdt (%s)", got, resp.PayURL)
	}
}

func TestEasyPayCustomMethodsResolveCIDFromConfiguredUpstreamType(t *testing.T) {
	t.Parallel()

	provider, err := NewEasyPay("test-instance", map[string]string{
		"pid":           "pid-1",
		"pkey":          "pkey-1",
		"apiBase":       "https://pay.example.com",
		"notifyUrl":     "https://example.com/notify",
		"returnUrl":     "https://example.com/return",
		"paymentMode":   paymentModePopup,
		"cidAlipay":     "cid-alipay",
		"cidWxpay":      "cid-wxpay",
		"customMethods": `[{"type":"ldc","upstreamType":"alipay","displayName":"LDC"}]`,
	})
	if err != nil {
		t.Fatalf("NewEasyPay: %v", err)
	}

	resp, err := provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{
		OrderID:     "sub2-custom-cid",
		Amount:      "1.00",
		PaymentType: "ldc",
		Subject:     "Custom EasyPay CID",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	payURL, err := url.Parse(resp.PayURL)
	if err != nil {
		t.Fatalf("parse pay url: %v", err)
	}
	if got := payURL.Query().Get("type"); got != "alipay" {
		t.Fatalf("pay url type = %q, want alipay (%s)", got, resp.PayURL)
	}
	if got := payURL.Query().Get("cid"); got != "cid-alipay" {
		t.Fatalf("pay url cid = %q, want cid-alipay (%s)", got, resp.PayURL)
	}
}

func TestEasyPaySupportedTypesIncludeCustomMethods(t *testing.T) {
	t.Parallel()

	provider, err := NewEasyPay("test-instance", map[string]string{
		"pid":           "pid-1",
		"pkey":          "pkey-1",
		"apiBase":       "https://pay.example.com",
		"notifyUrl":     "https://example.com/notify",
		"returnUrl":     "https://example.com/return",
		"customMethods": `[{"type":"ldc","upstreamType":"epay","displayName":"LDC"},{"type":"usdt_trc20","upstreamType":"usdt","displayName":"USDT-TRC20"}]`,
	})
	if err != nil {
		t.Fatalf("NewEasyPay: %v", err)
	}

	got := strings.Join(provider.SupportedTypes(), ",")
	for _, want := range []string{"alipay", "wxpay", "ldc", "usdt_trc20"} {
		if !strings.Contains(got, want) {
			t.Fatalf("SupportedTypes() = %q, want it to include %q", got, want)
		}
	}
}

func TestEasyPaySignConsistentOutput(t *testing.T) {
	t.Parallel()

	params := map[string]string{
		"pid":          "1001",
		"type":         "alipay",
		"out_trade_no": "ORDER123",
		"name":         "Test Product",
		"money":        "10.00",
	}
	pkey := "test_secret_key"

	sign1 := easyPaySign(params, pkey)
	sign2 := easyPaySign(params, pkey)
	if sign1 != sign2 {
		t.Fatalf("easyPaySign should be deterministic: %q != %q", sign1, sign2)
	}
	if len(sign1) != 32 {
		t.Fatalf("MD5 hex should be 32 chars, got %d", len(sign1))
	}
}

func TestEasyPaySignExcludesSignAndSignType(t *testing.T) {
	t.Parallel()

	pkey := "my_key"
	base := map[string]string{
		"pid":  "1001",
		"type": "alipay",
	}
	withSign := map[string]string{
		"pid":       "1001",
		"type":      "alipay",
		"sign":      "should_be_ignored",
		"sign_type": "MD5",
	}

	signBase := easyPaySign(base, pkey)
	signWithExtra := easyPaySign(withSign, pkey)

	if signBase != signWithExtra {
		t.Fatalf("sign and sign_type should be excluded: base=%q, withExtra=%q", signBase, signWithExtra)
	}
}

func TestEasyPaySignExcludesEmptyValues(t *testing.T) {
	t.Parallel()

	pkey := "key123"
	base := map[string]string{
		"pid":  "1001",
		"type": "alipay",
	}
	withEmpty := map[string]string{
		"pid":      "1001",
		"type":     "alipay",
		"device":   "",
		"clientip": "",
	}

	signBase := easyPaySign(base, pkey)
	signWithEmpty := easyPaySign(withEmpty, pkey)

	if signBase != signWithEmpty {
		t.Fatalf("empty values should be excluded: base=%q, withEmpty=%q", signBase, signWithEmpty)
	}
}

func TestEasyPayVerifySignValid(t *testing.T) {
	t.Parallel()

	params := map[string]string{
		"pid":          "1001",
		"type":         "alipay",
		"out_trade_no": "ORDER456",
		"money":        "25.00",
	}
	pkey := "secret"

	sign := easyPaySign(params, pkey)

	// Notifications carry both the signature and signature type.
	params["sign"] = sign
	params["sign_type"] = "MD5"

	if !easyPayVerifySign(params, pkey, sign) {
		t.Fatal("easyPayVerifySign should return true for a valid signature")
	}
}

func TestEasyPayVerifySignTampered(t *testing.T) {
	t.Parallel()

	params := map[string]string{
		"pid":          "1001",
		"type":         "alipay",
		"out_trade_no": "ORDER789",
		"money":        "50.00",
	}
	pkey := "secret"

	sign := easyPaySign(params, pkey)

	// Change the amount after computing the signature.
	params["money"] = "99.99"

	if easyPayVerifySign(params, pkey, sign) {
		t.Fatal("easyPayVerifySign should return false for tampered params")
	}
}

func TestEasyPayVerifySignWrongKey(t *testing.T) {
	t.Parallel()

	params := map[string]string{
		"pid":  "1001",
		"type": "wxpay",
	}

	sign := easyPaySign(params, "correct_key")

	if easyPayVerifySign(params, "wrong_key", sign) {
		t.Fatal("easyPayVerifySign should return false with wrong key")
	}
}

func TestEasyPaySignEmptyParams(t *testing.T) {
	t.Parallel()

	sign := easyPaySign(map[string]string{}, "key123")
	if sign == "" {
		t.Fatal("easyPaySign with empty params should still produce a hash")
	}
	if len(sign) != 32 {
		t.Fatalf("MD5 hex should be 32 chars, got %d", len(sign))
	}
}

func TestEasyPaySignSortOrder(t *testing.T) {
	t.Parallel()

	pkey := "test_key"
	params1 := map[string]string{
		"a": "1",
		"b": "2",
		"c": "3",
	}
	params2 := map[string]string{
		"c": "3",
		"a": "1",
		"b": "2",
	}

	sign1 := easyPaySign(params1, pkey)
	sign2 := easyPaySign(params2, pkey)

	if sign1 != sign2 {
		t.Fatalf("easyPaySign should be order-independent: %q != %q", sign1, sign2)
	}
}

func TestEasyPayVerifySignWrongSignValue(t *testing.T) {
	t.Parallel()

	params := map[string]string{
		"pid":  "1001",
		"type": "alipay",
	}
	pkey := "key"

	if easyPayVerifySign(params, pkey, "00000000000000000000000000000000") {
		t.Fatal("easyPayVerifySign should return false for an incorrect sign value")
	}
}

func TestEasyPayMerchantIdentityMetadata(t *testing.T) {
	t.Parallel()

	provider := &EasyPay{
		config: map[string]string{
			"pid": "1001",
		},
	}

	metadata := provider.MerchantIdentityMetadata()
	if metadata["pid"] != "1001" {
		t.Fatalf("pid = %q, want %q", metadata["pid"], "1001")
	}
}

// TestResolveEasyPayReturnedRef checks payment URL resolution rules (issue #6292).
// Unresolved paths become bare QR payloads or open on the gateway origin.
func TestResolveEasyPayReturnedRef(t *testing.T) {
	t.Parallel()

	const apiBase = "https://pay.example.com/xpay/epay"

	tests := []struct {
		name    string
		apiBase string
		ref     string
		want    string
	}{
		{
			// Resolve root-relative paths with the apiBase scheme and host.
			name:    "root relative path resolves against origin",
			apiBase: apiBase,
			ref:     "/api/pay/toapp/ORDER_ID",
			want:    "https://pay.example.com/api/pay/toapp/ORDER_ID",
		},
		{
			name:    "root relative path keeps query string",
			apiBase: apiBase,
			ref:     "/api/pay/toapp?oid=ORDER_ID&t=1",
			want:    "https://pay.example.com/api/pay/toapp?oid=ORDER_ID&t=1",
		},
		{
			name:    "protocol relative reference inherits base scheme",
			apiBase: apiBase,
			ref:     "//cashier.example.com/pay/ORDER_ID",
			want:    "https://cashier.example.com/pay/ORDER_ID",
		},
		{
			name:    "surrounding whitespace does not defeat resolution",
			apiBase: apiBase,
			ref:     "  /api/pay/toapp/ORDER_ID  ",
			want:    "https://pay.example.com/api/pay/toapp/ORDER_ID",
		},
		{
			name:    "http base keeps its scheme",
			apiBase: "http://pay.example.com",
			ref:     "/api/pay/toapp/ORDER_ID",
			want:    "http://pay.example.com/api/pay/toapp/ORDER_ID",
		},
		// These values are returned unchanged.
		{
			name:    "empty stays empty",
			apiBase: apiBase,
			ref:     "",
			want:    "",
		},
		{
			name:    "absolute https url is untouched",
			apiBase: apiBase,
			ref:     "https://cashier.other.com/pay/ORDER_ID",
			want:    "https://cashier.other.com/pay/ORDER_ID",
		},
		{
			name:    "wechat deep link is untouched",
			apiBase: apiBase,
			ref:     "weixin://wxpay/bizpayurl?pr=ABCdef",
			want:    "weixin://wxpay/bizpayurl?pr=ABCdef",
		},
		{
			name:    "alipay deep link is untouched",
			apiBase: apiBase,
			ref:     "alipays://platformapi/startapp?saId=10000007",
			want:    "alipays://platformapi/startapp?saId=10000007",
		},
		{
			name:    "face-to-face wxp payload is untouched",
			apiBase: apiBase,
			ref:     "wxp://f2f0Abc_dEfGhIjKlMnOpQrStU",
			want:    "wxp://f2f0Abc_dEfGhIjKlMnOpQrStU",
		},
		{
			// A token without a leading slash may be an opaque QR payload.
			name:    "opaque token without leading slash is untouched",
			apiBase: apiBase,
			ref:     "OrderToken123",
			want:    "OrderToken123",
		},
		{
			name:    "path-like reference without leading slash is untouched",
			apiBase: apiBase,
			ref:     "api/pay/toapp/ORDER_ID",
			want:    "api/pay/toapp/ORDER_ID",
		},
		{
			name:    "empty api base leaves the reference alone",
			apiBase: "",
			ref:     "/api/pay/toapp/ORDER_ID",
			want:    "/api/pay/toapp/ORDER_ID",
		},
		{
			name:    "api base without scheme leaves the reference alone",
			apiBase: "pay.example.com",
			ref:     "/api/pay/toapp/ORDER_ID",
			want:    "/api/pay/toapp/ORDER_ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveEasyPayReturnedRef(tt.apiBase, tt.ref); got != tt.want {
				t.Fatalf("resolveEasyPayReturnedRef(%q, %q) = %q, want %q", tt.apiBase, tt.ref, got, tt.want)
			}
		})
	}
}

func TestEasyPayCreatePaymentResolvesRelativeQRCode(t *testing.T) {
	t.Parallel()

	provider, origin := newEasyPayMapiStub(t, "/xpay/epay",
		`{"code":1,"trade_no":"TRADE_NO","payurl":"","qrcode":"/api/pay/toapp/ORDER_ID"}`)

	resp, err := provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{
		OrderID:     "sub2-relative-qr",
		Amount:      "1.00",
		PaymentType: payment.TypeWxpay,
		Subject:     "Relative QR",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if want := origin + "/api/pay/toapp/ORDER_ID"; resp.QRCode != want {
		t.Fatalf("QRCode = %q, want %q (a bare path would render as text in WeChat)", resp.QRCode, want)
	}
	if resp.PayURL != "" {
		t.Fatalf("PayURL = %q, want empty (upstream sent none)", resp.PayURL)
	}
	if resp.TradeNo != "TRADE_NO" {
		t.Fatalf("TradeNo = %q, want TRADE_NO", resp.TradeNo)
	}
}

func TestEasyPayCreatePaymentResolvesRelativeMobilePayURL2(t *testing.T) {
	t.Parallel()

	provider, origin := newEasyPayMapiStub(t, "",
		`{"code":1,"trade_no":"TRADE_NO","payurl":"/pc/pay/ORDER_ID","payurl2":"/h5/pay/ORDER_ID"}`)

	resp, err := provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{
		OrderID:     "sub2-relative-h5",
		Amount:      "1.00",
		PaymentType: payment.TypeWxpay,
		Subject:     "Relative H5",
		IsMobile:    true,
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	// Mobile checkout prefers payurl2 and resolves relative references.
	if want := origin + "/h5/pay/ORDER_ID"; resp.PayURL != want {
		t.Fatalf("PayURL = %q, want %q", resp.PayURL, want)
	}
}

func TestEasyPayCreatePaymentKeepsUpstreamAbsoluteAndOpaqueValues(t *testing.T) {
	t.Parallel()

	const (
		absoluteURL = "https://cashier.example.com/pay/ORDER_ID"
		deepLink    = "weixin://wxpay/bizpayurl?pr=ABCdef"
	)
	provider, _ := newEasyPayMapiStub(t, "/xpay/epay",
		`{"code":1,"trade_no":"TRADE_NO","payurl":"`+absoluteURL+`","qrcode":"`+deepLink+`"}`)

	resp, err := provider.CreatePayment(context.Background(), payment.CreatePaymentRequest{
		OrderID:     "sub2-absolute",
		Amount:      "1.00",
		PaymentType: payment.TypeWxpay,
		Subject:     "Absolute",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if resp.PayURL != absoluteURL {
		t.Fatalf("PayURL = %q, want %q unchanged", resp.PayURL, absoluteURL)
	}
	if resp.QRCode != deepLink {
		t.Fatalf("QRCode = %q, want %q unchanged", resp.QRCode, deepLink)
	}
}

// easyPayTestNotificationValues returns unsigned standard notification fields for validation tests.
func easyPayTestNotificationValues() url.Values {
	return url.Values{
		"pid": {"1000"}, "trade_no": {"UPSTREAM123"}, "out_trade_no": {"ORDER123"},
		"type": {"alipay"}, "name": {"充值 & 套餐=10%"}, "money": {"650.00"},
		"trade_status": {tradeStatusSuccess},
	}
}

// newTestEasyPay creates a provider with test merchant credentials.
func newTestEasyPay(t *testing.T, apiBase string) *EasyPay {
	t.Helper()

	provider, err := NewEasyPay("test-instance", map[string]string{
		"pid":       "pid-1",
		"pkey":      "pkey-1",
		"apiBase":   apiBase,
		"notifyUrl": "https://example.com/notify",
		"returnUrl": "https://example.com/return",
	})
	if err != nil {
		t.Fatalf("NewEasyPay: %v", err)
	}
	return provider
}

// newEasyPayMapiStub returns a provider with a fixed mapi.php response and its test server URL.
// basePath exercises root-relative URL resolution.
func newEasyPayMapiStub(t *testing.T, basePath, body string) (*EasyPay, string) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	apiBase := server.URL + basePath
	provider, err := NewEasyPay("test-instance", map[string]string{
		"pid":       "pid-1",
		"pkey":      "pkey-1",
		"apiBase":   apiBase,
		"notifyUrl": "https://example.com/notify",
		"returnUrl": "https://example.com/return",
	})
	if err != nil {
		t.Fatalf("NewEasyPay: %v", err)
	}
	return provider, server.URL
}
