package provider

import (
	"context"
	"errors"
	"fmt"

	captcha "github.com/alibabacloud-go/captcha-20230305/client"
	openapiutil "github.com/alibabacloud-go/darabonba-openapi/v2/utils"
	"github.com/alibabacloud-go/tea/dara"
	"github.com/alibabacloud-go/tea/tea"

	"github.com/TokenFlux/TokenRouter/internal/identity"
)

// aliyun_captcha_verifier.go submits Aliyun CAPTCHA proofs and translates SDK
// responses and errors for the identity service.
// Aliyun challenge decisions arrive inside an SDK response whose transport and
// API errors use different representations. The adapter supplies current
// credentials on each call and converts known SDK API errors for the identity
// service, which decides whether the returned challenge result permits the action.

// aliyunCaptchaTimeoutMillis bounds the SDK connection and response-read waits
// in milliseconds for each newly created verification client.
const aliyunCaptchaTimeoutMillis = 10_000

// aliyunCaptchaVerifier configures the SDK transport used for each challenge check.
type aliyunCaptchaVerifier struct {
	protocol      string // SDK request protocol selection
	timeoutMillis int    // milliseconds
}

// NewAliyunCaptchaVerifier constructs an HTTPS challenge adapter with ten-second
// SDK connection and read budgets. Credentials and the regional endpoint are
// supplied to VerifyCaptcha separately for each attempt.
func NewAliyunCaptchaVerifier() identity.AliyunCaptchaVerifier {
	return &aliyunCaptchaVerifier{
		protocol:      "HTTPS",
		timeoutMillis: aliyunCaptchaTimeoutMillis,
	}
}

// VerifyCaptcha sends the supplied scene and challenge parameter to Aliyun using
// that call's credentials. It returns the provider decision for the identity
// service to interpret; a missing body result keeps the false, empty decision.
func (v *aliyunCaptchaVerifier) VerifyCaptcha(ctx context.Context, cred identity.AliyunCaptchaCredentials, captchaVerifyParam string) (*identity.AliyunCaptchaVerifyResult, error) {
	// Construct per call so an administrator's credential or endpoint update is
	// used by the next challenge, rather than a previously initialized SDK client.
	client, err := captcha.NewClient(&openapiutil.Config{
		AccessKeyId:     dara.String(cred.AccessKeyID),
		AccessKeySecret: dara.String(cred.AccessKeySecret),
		Endpoint:        dara.String(cred.Endpoint),
		Protocol:        dara.String(v.protocol),
		ConnectTimeout:  dara.Int(v.timeoutMillis),
		ReadTimeout:     dara.Int(v.timeoutMillis),
	})
	if err != nil {
		return nil, fmt.Errorf("create aliyun captcha client: %w", err)
	}

	request := &captcha.VerifyIntelligentCaptchaRequest{
		CaptchaVerifyParam: dara.String(captchaVerifyParam),
		SceneId:            dara.String(cred.SceneID),
	}

	response, err := client.VerifyIntelligentCaptchaWithContext(ctx, request, &dara.RuntimeOptions{})
	if err != nil {
		// Normalize known SDK API failures because the core distinguishes provider
		// configuration errors from challenge rejection using the returned error code.
		return nil, normalizeAliyunCaptchaError(err)
	}

	result := &identity.AliyunCaptchaVerifyResult{}
	if body := response.Body; body != nil && body.Result != nil {
		result.VerifyResult = dara.BoolValue(body.Result.VerifyResult)
		result.VerifyCode = dara.StringValue(body.Result.VerifyCode)
	}
	return result, nil
}

// normalizeAliyunCaptchaError extracts the code and message from either supported
// SDK API error representation. Matching wrappers are inspected with errors.AsType;
// other failures are returned as received for the core's transport-error handling.
func normalizeAliyunCaptchaError(err error) error {
	if teaErr, ok := errors.AsType[*tea.SDKError](err); ok {
		return &identity.AliyunCaptchaAPIError{
			Code:    tea.StringValue(teaErr.Code),
			Message: tea.StringValue(teaErr.Message),
		}
	}
	if daraErr, ok := errors.AsType[*dara.SDKError](err); ok {
		return &identity.AliyunCaptchaAPIError{
			Code:    dara.StringValue(daraErr.Code),
			Message: dara.StringValue(daraErr.Message),
		}
	}
	return err
}
