package codeassist

import (
	"encoding/json"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/protocol/google"
)

// activation.go identifies disabled Google services and extracts activation
// links from Code Assist error details.
// Code Assist account operations can fail with a disabled-service error that
// carries a console activation link. Error details are decoded into metadata
// and help variants, while the status and reason classifier lets
// account-management callers distinguish this setup problem from other
// permission failures.

// ExtractActivationURL selects the first metadata activationUrl or matching
// help link in detail order. Invalid JSON or absent matching details returns
// an empty string; returned links retain upstream text and receive no URL
// validation here.
func ExtractActivationURL(body string) string {
	// Malformed envelopes provide no trustworthy activation detail; an empty
	// observation lets account callers use their fixed console guidance.
	var errResp google.ErrorResponse
	if err := json.Unmarshal([]byte(body), &errResp); err != nil {
		return ""
	}

	// Try metadata and help decoding for each detail because Google error
	// variants carry activation guidance in either shape, and absent fields allow
	// the next detail.
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

// IsServiceDisabledError recognizes a decoded 403 PERMISSION_DENIED envelope
// containing a SERVICE_DISABLED detail reason. Invalid JSON and other statuses
// return false; the detail reason is checked independently of its type marker.
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
