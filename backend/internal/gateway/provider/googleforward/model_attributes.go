package googleforward

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
)

// applyGeminiModelParameters 使用最终路线设置生成参数，控制字段来自客户端原始协议。
func applyGeminiModelParameters(body, controls []byte, resolution modelcatalog.Resolution, convertThinking bool) ([]byte, error) {
	result := resolution.ForRequest(gatewayprovider.ModelRequestState(controls))
	var err error
	if convertThinking {
		thinking, thinkingErr := gatewayprovider.GeminiThinking(resolution, controls)
		if thinkingErr != nil {
			return nil, thinkingErr
		}
		if thinking != nil {
			body, err = sjson.SetBytes(body, "generationConfig.thinkingConfig", thinking)
		}
		if err != nil {
			return nil, err
		}
	}
	for key, support := range map[string]*bool{"temperature": result.Attributes.Temperature, "topP": result.Attributes.TopP} {
		if support == nil || *support {
			continue
		}
		for _, config := range []string{"generationConfig", "generation_config"} {
			if gjson.GetBytes(body, config+"."+key).Exists() {
				body, err = sjson.DeleteBytes(body, config+"."+key)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	if resolution.Target.Platform == "antigravity" && resolution.Target.ClientProtocol != "gemini_generate_content" {
		if limit := result.Attributes.OutputLimit; limit != nil && gjson.GetBytes(body, "generationConfig.maxOutputTokens").Int() > int64(*limit) {
			body, err = sjson.SetBytes(body, "generationConfig.maxOutputTokens", *limit)
		}
	}
	return body, err
}
