package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// ensureQoderMachineCredentials fills the record with a stable machine identity for its site.
// Direct-token callers supply machine_id; the CN site uses no additional machine fields.
func ensureQoderMachineCredentials(provider *provider.Record) {
	if provider == nil {
		return
	}
	if provider.Credentials == nil {
		provider.Credentials = make(map[string]any)
	}
	pat := strings.TrimSpace(provider.GetCredential("pat"))
	directToken := strings.TrimSpace(provider.GetCredential("security_oauth_token"))
	machineID := strings.TrimSpace(provider.GetCredential("machine_id"))
	if pat == "" && (directToken == "" || machineID == "") {
		return
	}
	site, err := qoderSiteForRecord(provider)
	if err != nil {
		site = qoder.SiteGlobal
	}
	if site == qoder.SiteCN {
		// CN credentials store machine_id alone; remove legacy random machine fields.
		delete(provider.Credentials, "machine_token")
		delete(provider.Credentials, "machine_type")
		if pat != "" && machineID == "" {
			provider.Credentials["machine_id"] = qoder.NewMachineForSite(site).MachineID
		}
		return
	}
	machine := qoder.NewMachineForSite(site)
	if machineID != "" && strings.TrimSpace(provider.GetCredential("machine_token")) != "" &&
		strings.TrimSpace(provider.GetCredential("machine_type")) != "" {
		return
	}
	if pat != "" && machineID == "" {
		provider.Credentials["machine_id"] = machine.MachineID
	}
	if strings.TrimSpace(provider.GetCredential("machine_token")) == "" {
		provider.Credentials["machine_token"] = machine.MachineToken
	}
	if strings.TrimSpace(provider.GetCredential("machine_type")) == "" {
		provider.Credentials["machine_type"] = machine.MachineType
	}
}
