package postgres

import (
	"context"

	"github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/tlsfingerprintprofile"
	"github.com/TokenFlux/TokenRouter/internal/egress"
)

// tls_profile_repo.go persists TLS fingerprint profiles and converts stored
// optional lists for administrator reads.
// Stored TLS profiles expose arrays to administrator responses even when
// database fields are absent. Creation omits empty optional lists, update
// clears them, and entity conversion fills absent arrays with present empty
// values before later service cloning provides runtime snapshot ownership.

// tlsFingerprintProfileRepository maps Ent profile persistence into editable
// egress fingerprint values.
type tlsFingerprintProfileRepository struct {
	// client remains available while profile administration and cache reloads use
	// this adapter. The application owns its lifecycle and keeps persistence open
	// until dependent readers stop.
	client *ent.Client // borrowed application Ent client
}

// NewTLSFingerprintProfileRepository creates a profile persistence adapter
// using the supplied Ent client. The adapter retains that client for later
// commands, and its application owner keeps it available throughout
// administration and cache reloads.
func NewTLSFingerprintProfileRepository(client *ent.Client) egress.TLSFingerprintProfileRepository {
	return &tlsFingerprintProfileRepository{client: client}
}

// List returns all persisted TLS profiles ordered by their stored name.
// Conversion fills absent arrays for JSON responses, while nested nonempty
// values can retain the loaded entity's storage until service cloning.
func (r *tlsFingerprintProfileRepository) List(ctx context.Context) ([]*egress.TLSFingerprintProfile, error) {
	profiles, err := r.client.TLSFingerprintProfile.Query().
		Order(ent.Asc(tlsfingerprintprofile.FieldName)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]*egress.TLSFingerprintProfile, len(profiles))
	for i, p := range profiles {
		result[i] = r.toModel(p)
	}
	return result, nil
}

// GetByID returns the stored profile, or nil with no error when Ent reports its
// ID missing. Other persistence errors return directly; conversion supplies
// present empty arrays for absent list fields.
func (r *tlsFingerprintProfileRepository) GetByID(ctx context.Context, id int64) (*egress.TLSFingerprintProfile, error) {
	p, err := r.client.TLSFingerprintProfile.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return r.toModel(p), nil
}

// Create persists the supplied name, GREASE flag, and populated optional
// profile values. Empty lists are omitted from the create builder; the returned
// egress model includes database metadata and converts absent arrays to present
// empty slices.
func (r *tlsFingerprintProfileRepository) Create(ctx context.Context, p *egress.TLSFingerprintProfile) (*egress.TLSFingerprintProfile, error) {
	builder := r.client.TLSFingerprintProfile.Create().
		SetName(p.Name).
		SetEnableGrease(p.EnableGREASE)

	if p.Description != nil {
		builder.SetDescription(*p.Description)
	}
	if len(p.CipherSuites) > 0 {
		builder.SetCipherSuites(p.CipherSuites)
	}
	if len(p.Curves) > 0 {
		builder.SetCurves(p.Curves)
	}
	if len(p.PointFormats) > 0 {
		builder.SetPointFormats(p.PointFormats)
	}
	if len(p.SignatureAlgorithms) > 0 {
		builder.SetSignatureAlgorithms(p.SignatureAlgorithms)
	}
	if len(p.ALPNProtocols) > 0 {
		builder.SetAlpnProtocols(p.ALPNProtocols)
	}
	if len(p.SupportedVersions) > 0 {
		builder.SetSupportedVersions(p.SupportedVersions)
	}
	if len(p.KeyShareGroups) > 0 {
		builder.SetKeyShareGroups(p.KeyShareGroups)
	}
	if len(p.PSKModes) > 0 {
		builder.SetPskModes(p.PSKModes)
	}
	if len(p.Extensions) > 0 {
		builder.SetExtensions(p.Extensions)
	}

	created, err := builder.Save(ctx)
	if err != nil {
		return nil, err
	}
	return r.toModel(created), nil
}

// Update replaces the profile identified by ID with the supplied editable
// values. Nil description and empty lists clear their stored fields, so callers
// supply a complete desired profile after merging any partial HTTP update.
func (r *tlsFingerprintProfileRepository) Update(ctx context.Context, p *egress.TLSFingerprintProfile) (*egress.TLSFingerprintProfile, error) {
	builder := r.client.TLSFingerprintProfile.UpdateOneID(p.ID).
		SetName(p.Name).
		SetEnableGrease(p.EnableGREASE)

	// Update receives a complete merged profile. Clear absent optional values
	// here so an explicit empty list reaches persistence and later runtime defaults.
	if p.Description != nil {
		builder.SetDescription(*p.Description)
	} else {
		builder.ClearDescription()
	}

	if len(p.CipherSuites) > 0 {
		builder.SetCipherSuites(p.CipherSuites)
	} else {
		builder.ClearCipherSuites()
	}
	if len(p.Curves) > 0 {
		builder.SetCurves(p.Curves)
	} else {
		builder.ClearCurves()
	}
	if len(p.PointFormats) > 0 {
		builder.SetPointFormats(p.PointFormats)
	} else {
		builder.ClearPointFormats()
	}
	if len(p.SignatureAlgorithms) > 0 {
		builder.SetSignatureAlgorithms(p.SignatureAlgorithms)
	} else {
		builder.ClearSignatureAlgorithms()
	}
	if len(p.ALPNProtocols) > 0 {
		builder.SetAlpnProtocols(p.ALPNProtocols)
	} else {
		builder.ClearAlpnProtocols()
	}
	if len(p.SupportedVersions) > 0 {
		builder.SetSupportedVersions(p.SupportedVersions)
	} else {
		builder.ClearSupportedVersions()
	}
	if len(p.KeyShareGroups) > 0 {
		builder.SetKeyShareGroups(p.KeyShareGroups)
	} else {
		builder.ClearKeyShareGroups()
	}
	if len(p.PSKModes) > 0 {
		builder.SetPskModes(p.PSKModes)
	} else {
		builder.ClearPskModes()
	}
	if len(p.Extensions) > 0 {
		builder.SetExtensions(p.Extensions)
	} else {
		builder.ClearExtensions()
	}

	updated, err := builder.Save(ctx)
	if err != nil {
		return nil, err
	}
	return r.toModel(updated), nil
}

// Delete removes the stored TLS fingerprint profile identified by id.
func (r *tlsFingerprintProfileRepository) Delete(ctx context.Context, id int64) error {
	return r.client.TLSFingerprintProfile.DeleteOneID(id).Exec(ctx)
}

// toModel converts a nonnil Ent profile into egress values and supplies present
// empty arrays for absent lists. Optional description pointers and populated
// list storage remain shared with the loaded entity; runtime service
// publication subsequently clones those values.
func (r *tlsFingerprintProfileRepository) toModel(e *ent.TLSFingerprintProfile) *egress.TLSFingerprintProfile {
	p := &egress.TLSFingerprintProfile{
		ID:                  e.ID,
		Name:                e.Name,
		Description:         e.Description,
		EnableGREASE:        e.EnableGrease,
		CipherSuites:        e.CipherSuites,
		Curves:              e.Curves,
		PointFormats:        e.PointFormats,
		SignatureAlgorithms: e.SignatureAlgorithms,
		ALPNProtocols:       e.AlpnProtocols,
		SupportedVersions:   e.SupportedVersions,
		KeyShareGroups:      e.KeyShareGroups,
		PSKModes:            e.PskModes,
		Extensions:          e.Extensions,
		CreatedAt:           e.CreatedAt,
		UpdatedAt:           e.UpdatedAt,
	}

	// Administrative JSON expects arrays for each fingerprint list. Replace
	// absent entity slices with present empty values before the response is encoded.
	if p.CipherSuites == nil {
		p.CipherSuites = []uint16{}
	}
	if p.Curves == nil {
		p.Curves = []uint16{}
	}
	if p.PointFormats == nil {
		p.PointFormats = []uint16{}
	}
	if p.SignatureAlgorithms == nil {
		p.SignatureAlgorithms = []uint16{}
	}
	if p.ALPNProtocols == nil {
		p.ALPNProtocols = []string{}
	}
	if p.SupportedVersions == nil {
		p.SupportedVersions = []uint16{}
	}
	if p.KeyShareGroups == nil {
		p.KeyShareGroups = []uint16{}
	}
	if p.PSKModes == nil {
		p.PSKModes = []uint16{}
	}
	if p.Extensions == nil {
		p.Extensions = []uint16{}
	}

	return p
}
