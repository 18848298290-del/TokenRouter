package provider

import (
	"context"
	"fmt"

	captcha "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/captcha/v20190722"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// tencent_captcha_service.go submits Tencent CAPTCHA tickets through a
// per-call SDK client and returns provider verification results.
// Tencent verification uses both cloud signing credentials and a CAPTCHA
// application secret selected by the identity service. A fresh SDK client receives
// that call's regional endpoint, and the returned code and message remain provider
// results for the core to classify.

// tencentCaptchaAPI exposes the SDK request used to verify one CAPTCHA ticket.
type tencentCaptchaAPI interface {
	// DescribeCaptchaResultWithContext submits the populated ticket request using
	// the caller's context. Implementations return an SDK response or request error;
	// the adapter rejects missing response envelopes before reading fields.
	DescribeCaptchaResultWithContext(context.Context, *captcha.DescribeCaptchaResultRequest) (*captcha.DescribeCaptchaResultResponse, error)
}

// tencentCaptchaClientFactory constructs an API client from current credentials
// and the endpoint selected for this verification attempt.
type tencentCaptchaClientFactory func(secretID, secretKey, endpoint string) (tencentCaptchaAPI, error)

// tencentCaptchaVerifier converts identity challenge inputs into Tencent SDK requests.
type tencentCaptchaVerifier struct {
	newClient tencentCaptchaClientFactory // per-call SDK client constructor
}

// NewTencentCaptchaVerifier constructs a ticket adapter using the Tencent SDK factory.
func NewTencentCaptchaVerifier() identity.TencentCaptchaVerifier {
	return &tencentCaptchaVerifier{newClient: newTencentCaptchaSDKClient}
}

// newTencentCaptchaSDKClient creates a signed POST client with a five-second
// request budget. The identity service supplies the region's endpoint; an empty
// endpoint lets the SDK choose captcha.tencentcloudapi.com for the China region.
func newTencentCaptchaSDKClient(secretID, secretKey, endpoint string) (tencentCaptchaAPI, error) {
	clientProfile := profile.NewClientProfile()
	clientProfile.HttpProfile.Endpoint = endpoint
	clientProfile.HttpProfile.ReqMethod = "POST"
	// Bound verification waits in the SDK's seconds unit.
	clientProfile.HttpProfile.ReqTimeout = 5
	return captcha.NewClient(common.NewCredential(secretID, secretKey), "", clientProfile)
}

// VerifyTicket submits the ticket, random string, and client IP with the supplied
// application and cloud credentials. Missing response envelopes fail; absent code
// or message pointers become zero or empty values for core classification.
func (v *tencentCaptchaVerifier) VerifyTicket(ctx context.Context, credentials identity.TencentCaptchaCredentials, proof identity.TencentCaptchaProof, remoteIP string) (*identity.TencentCaptchaVerifyResponse, error) {
	client, err := v.newClient(credentials.CloudSecretID, credentials.CloudSecretKey, credentials.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("create tencent captcha client: %w", err)
	}
	request := captcha.NewDescribeCaptchaResultRequest()
	// The SDK request schema requires the fixed CAPTCHA type value 9.
	request.CaptchaType = common.Uint64Ptr(9)
	request.Ticket = common.StringPtr(proof.Ticket)
	request.UserIp = common.StringPtr(remoteIP)
	request.Randstr = common.StringPtr(proof.Randstr)
	request.CaptchaAppId = common.Uint64Ptr(credentials.AppID)
	request.AppSecretKey = common.StringPtr(credentials.AppSecretKey)

	response, err := client.DescribeCaptchaResultWithContext(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("describe captcha result: %w", err)
	}
	if response == nil || response.Response == nil {
		return nil, fmt.Errorf("describe captcha result: empty response")
	}
	return &identity.TencentCaptchaVerifyResponse{
		CaptchaCode: valueOrZero(response.Response.CaptchaCode),
		CaptchaMsg:  valueOrEmpty(response.Response.CaptchaMsg),
		RequestID:   valueOrEmpty(response.Response.RequestId),
	}, nil
}

// valueOrZero reads an optional SDK integer for identity response mapping. An
// absent pointer becomes zero, which the core treats as a provider decision code
// rather than a transport failure.
func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

// valueOrEmpty reads an optional SDK message or request identifier for response
// mapping. An absent pointer becomes empty text so the adapter can return the
// available decision fields together.
func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
