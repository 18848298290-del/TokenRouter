package provider

import (
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/upstream/qoder"
)

// qoder_machine.go prepares site-specific Qoder machine credentials for Global
// and CN exchanges. Qoder Global exchanges use machine_id, machine_token and
// machine_type, while CN keeps machine_id alone. Imported CN credentials can
// retain the additional Global fields, so preparation removes them before
// later builders assemble the site-specific session. A supplied nonblank
// machine_id stays on the record even when the helper fills other missing
// Global fields.
//
// PAT input may create a missing ID; direct-token preparation starts when a
// security token and machine_id are already present. The helper edits
// Credentials in place, and creation hooks clone supported containers before
// calling it. Unsupported site input takes the Global preparation path, with
// site rejection performed separately during validation.

// ensureQoderMachineCredentials fills supported machine fields in the supplied
// record.
// Nil records are ignored and missing credential maps are allocated. PAT input
// can receive a generated machine_id; direct-token input requires an existing
// machine_id before preparation. Invalid site values use Global preparation,
// while CN removes legacy machine_token and machine_type. Callers serialize
// mutation of the record and its credential map during this preparation.
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
	// This preparation helper keeps the historical Global shape for invalid site
	// input; creation and edit validation separately report unsupported site
	// selections.
	if err != nil {
		site = qoder.SiteGlobal
	}
	if site == qoder.SiteCN {
		// The CN exchange uses its UUID machine_id and omits the other machine
		// headers.
		// Drop imported legacy fields so later credential builders use that
		// site-specific shape.
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
