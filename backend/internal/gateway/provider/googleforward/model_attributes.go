package googleforward

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	gatewayprovider "github.com/TokenFlux/TokenRouter/internal/gateway/provider"
	"github.com/TokenFlux/TokenRouter/internal/modelcatalog"
)

// applyGeminiModelParameters applies final-route capabilities to controls from the original client protocol.
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
	for _, configName := range []string{"generationConfig", "generation_config"} {
		config := gjson.GetBytes(body, configName)
		for key, support := range map[string]*bool{"temperature": result.Attributes.Temperature, "topP": result.Attributes.TopP, "top_p": result.Attributes.TopP} {
			if support == nil || *support || !config.Get(key).Exists() {
				continue
			}
			body, err = sjson.DeleteBytes(body, configName+"."+key)
			if err != nil {
				return nil, err
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
