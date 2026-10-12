package composite

import (
	"context"
	"fmt"
	"maps"

	"github.com/TokenFlux/TokenRouter/internal/gateway"
	"github.com/TokenFlux/TokenRouter/internal/gateway/tierpolicy"
	"github.com/TokenFlux/TokenRouter/internal/identity"
	"github.com/TokenFlux/TokenRouter/internal/settings"
)

// Runtime coordinates settings reads, preparation, and ordered application through the owning modules.
type Runtime struct {
	store          *settings.Store
	read           ReadOptions
	prepare        PrepareOptions
	grants         *identity.GrantSettings
	gateway        *gateway.RuntimeSettings
	totpConfigured bool
	applications   []Application
}

func NewRuntime(store *settings.Store, read ReadOptions, prepare PrepareOptions, grants *identity.GrantSettings, gateway *gateway.RuntimeSettings, totpConfigured bool, applications []Application) *Runtime {
	return &Runtime{store: store, read: read, prepare: prepare, grants: grants, gateway: gateway, totpConfigured: totpConfigured, applications: append([]Application(nil), applications...)}
}

func (s *Runtime) GetAllSettings(ctx context.Context) (*Snapshot, error) {
	values, err := s.store.GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("get all settings: %w", err)
	}
	return Parse(values, s.read), nil
}

func (s *Runtime) GetAuthSourceDefaultSettings(ctx context.Context) (*identity.AuthSourceDefaultSettings, error) {
	return s.grants.GetAuthSourceDefaultSettings(ctx)
}

func (s *Runtime) GetOpenAIFastPolicySettings(ctx context.Context) (*tierpolicy.OpenAIFastPolicySettings, error) {
	return s.gateway.GetOpenAIFastPolicySettings(ctx)
}
func (s *Runtime) IsTotpEncryptionKeyConfigured() bool { return s.totpConfigured }
func (s *Runtime) OIDCSecurityWriteDefaults(ctx context.Context) (bool, bool, error) {
	return s.read.OAuth.OIDCSecurityWriteDefaults(ctx)
}

func (s *Runtime) BeginSettingsUpdate(ctx context.Context) (*settings.UpdateSession, error) {
	return s.store.Updates().Begin(ctx)
}

func (s *Runtime) PrepareSettingsWithAuthSourceDefaults(ctx context.Context, value *Snapshot, auth *identity.AuthSourceDefaultSettings, omitted settings.OmittedKeys) (map[string]string, error) {
	values, err := Prepare(ctx, value, s.prepare)
	if err != nil {
		return nil, err
	}
	authValues, err := s.grants.PrepareAuthSourceDefaults(ctx, auth)
	if err != nil {
		return nil, err
	}
	maps.Copy(values, authValues)
	omitted.DropFrom(values)
	return values, nil
}

func (s *Runtime) ApplicationChanges() []settings.PreparedChange {
	return ApplicationsForUpdate(s.GetAllSettings, s.applications)
}

// ApplyPersistedSettings reads committed settings and applies modules in order, returning the first error.
func (s *Runtime) ApplyPersistedSettings(ctx context.Context) error {
	value, err := s.GetAllSettings(ctx)
	if err != nil {
		return err
	}
	for _, step := range s.applications {
		if err := step.Apply(ctx, value); err != nil {
			return err
		}
	}
	return nil
}
