package provider

import (
	"context"
	"fmt"

	captcha "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/captcha/v20190722"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

type tencentCaptchaAPI interface {
	DescribeCaptchaResultWithContext(context.Context, *captcha.DescribeCaptchaResultRequest) (*captcha.DescribeCaptchaResultResponse, error)
}

type tencentCaptchaClientFactory func(secretID, secretKey, endpoint string) (tencentCaptchaAPI, error)

type tencentCaptchaVerifier struct {
	newClient tencentCaptchaClientFactory
}

// NewTencentCaptchaVerifier creates a Tencent captcha ticket verifier.
func NewTencentCaptchaVerifier() identity.TencentCaptchaVerifier {
	return &tencentCaptchaVerifier{newClient: newTencentCaptchaSDKClient}
}

// newTencentCaptchaSDKClient uses the endpoint selected for the configured region.
// The service supplies a nonempty endpoint.
// An empty SDK endpoint defaults to captcha.tencentcloudapi.com, the China region.
func newTencentCaptchaSDKClient(secretID, secretKey, endpoint string) (tencentCaptchaAPI, error) {
	clientProfile := profile.NewClientProfile()
	clientProfile.HttpProfile.Endpoint = endpoint
	clientProfile.HttpProfile.ReqMethod = "POST"
	clientProfile.HttpProfile.ReqTimeout = 5
	return captcha.NewClient(common.NewCredential(secretID, secretKey), "", clientProfile)
}

func (v *tencentCaptchaVerifier) VerifyTicket(ctx context.Context, credentials identity.TencentCaptchaCredentials, proof identity.TencentCaptchaProof, remoteIP string) (*identity.TencentCaptchaVerifyResponse, error) {
	client, err := v.newClient(credentials.CloudSecretID, credentials.CloudSecretKey, credentials.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("create tencent captcha client: %w", err)
	}
	request := captcha.NewDescribeCaptchaResultRequest()
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

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
