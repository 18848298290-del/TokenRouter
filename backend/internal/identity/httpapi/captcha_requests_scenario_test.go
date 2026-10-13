package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// Tencent action proof travels through the JSON request types in
// session_handler.go, pending_handler.go, oauth_start.go, and
// passkey_handler.go. These cases decode the same payload into each request
// and inspect ticket and random-string transport before any provider
// verification runs.

// TestAuthRequestsBindTencentCaptchaProof decodes one Tencent ticket payload
// through eight authentication request types. It checks that each resulting
// proof retains the exact ticket and random string.
func TestAuthRequestsBindTencentCaptchaProof(t *testing.T) {
	const payload = `{"email":"user@example.com","password":"secret-123","tencent_captcha_ticket":"ticket-value","tencent_captcha_randstr":"@rand-value"}`

	tests := []struct {
		name   string                             // request fixture case
		decode func([]byte) identity.CaptchaProof // request proof decoder callback
	}{
		{
			name: "登录",
			decode: func(raw []byte) identity.CaptchaProof {
				var req LoginRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				return identity.CaptchaProof{TurnstileToken: req.TurnstileToken, TencentTicket: req.TencentCaptchaTicket, TencentRandstr: req.TencentCaptchaRandstr}
			},
		},
		{
			name: "注册",
			decode: func(raw []byte) identity.CaptchaProof {
				var req RegisterRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				return identity.CaptchaProof{TurnstileToken: req.TurnstileToken, TencentTicket: req.TencentCaptchaTicket, TencentRandstr: req.TencentCaptchaRandstr}
			},
		},
		{
			name: "发送邮箱验证码",
			decode: func(raw []byte) identity.CaptchaProof {
				var req SendVerifyCodeRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				return identity.CaptchaProof{TurnstileToken: req.TurnstileToken, TencentTicket: req.TencentCaptchaTicket, TencentRandstr: req.TencentCaptchaRandstr}
			},
		},
		{
			name: "忘记密码",
			decode: func(raw []byte) identity.CaptchaProof {
				var req ForgotPasswordRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				return identity.CaptchaProof{TurnstileToken: req.TurnstileToken, TencentTicket: req.TencentCaptchaTicket, TencentRandstr: req.TencentCaptchaRandstr}
			},
		},
		{
			name: "OAuth启动",
			decode: func(raw []byte) identity.CaptchaProof {
				var req OAuthStartCaptchaRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				return identity.CaptchaProof{TurnstileToken: req.TurnstileToken, TencentTicket: req.TencentCaptchaTicket, TencentRandstr: req.TencentCaptchaRandstr}
			},
		},
		{
			name: "Passkey登录",
			decode: func(raw []byte) identity.CaptchaProof {
				var req PasskeyBeginLoginRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				return identity.CaptchaProof{TurnstileToken: req.TurnstileToken, TencentTicket: req.TencentCaptchaTicket, TencentRandstr: req.TencentCaptchaRandstr}
			},
		},
		{
			name: "OAuth待处理账号发送邮箱验证码",
			decode: func(raw []byte) identity.CaptchaProof {
				var req SendPendingOAuthVerifyCodeRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				return identity.CaptchaProof{TurnstileToken: req.TurnstileToken, TencentTicket: req.TencentCaptchaTicket, TencentRandstr: req.TencentCaptchaRandstr}
			},
		},
		{
			name: "OAuth待处理账号创建",
			decode: func(raw []byte) identity.CaptchaProof {
				var req CreatePendingOAuthAccountRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				return identity.CaptchaProof{TurnstileToken: req.TurnstileToken, TencentTicket: req.TencentCaptchaTicket, TencentRandstr: req.TencentCaptchaRandstr}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proof := test.decode([]byte(payload))
			require.Equal(t, "ticket-value", proof.TencentTicket)
			require.Equal(t, "@rand-value", proof.TencentRandstr)
		})
	}
}
