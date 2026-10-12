package codeassist

import (
	"encoding/json"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/protocol/google"
)

// ExtractActivationURL returns an activation link from error metadata or help details.
func ExtractActivationURL(body string) string {
	var errResp google.ErrorResponse
	if err := json.Unmarshal([]byte(body), &errResp); err != nil {
		return ""
	}

	for _, detailRaw := range errResp.Error.Details {
		var info google.ErrorDetailInfo
		if err := json.Unmarshal(detailRaw, &info); err == nil {
			if info.Metadata != nil {
				if activationURL, ok := info.Metadata["activationUrl"]; ok && activationURL != "" {
					return activationURL
				}
			}
		}

		var help google.ErrorHelp
		if err := json.Unmarshal(detailRaw, &help); err == nil {
			for _, link := range help.Links {
				if strings.Contains(link.Description, "activation") ||
					strings.Contains(link.Description, "API activation") ||
					strings.Contains(link.URL, "/apis/api/") {
					return link.URL
				}
			}
		}
	}

	return ""
}

// IsServiceDisabledError reports whether a 403 PERMISSION_DENIED response has a SERVICE_DISABLED reason.
func IsServiceDisabledError(body string) bool {
	var errResp google.ErrorResponse
	if err := json.Unmarshal([]byte(body), &errResp); err != nil {
		return false
	}

	if errResp.Error.Code != 403 || errResp.Error.Status != "PERMISSION_DENIED" {
		return false
	}

	for _, detailRaw := range errResp.Error.Details {
		var info google.ErrorDetailInfo
		if err := json.Unmarshal(detailRaw, &info); err == nil {
			if info.Reason == "SERVICE_DISABLED" {
				return true
			}
		}
	}

	return false
}
