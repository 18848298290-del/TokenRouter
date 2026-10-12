//go:build integration

package provider_test

import (
	"context"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/billing"
	"github.com/TokenFlux/TokenRouter/internal/provider"
	"github.com/TokenFlux/TokenRouter/internal/routing/capability"
)

func TestProviderRepoSparkShadowRoundTrip(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newProviderStoreContract(tx.Client(), tx, nil)

	parent := &provider.Record{
		Name:     "parent",
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Status:   billing.StatusActive,
	}
	if err := repo.Create(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	pid := parent.ID
	shadow := &provider.Record{
		Name:             "shadow",
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		Status:           billing.StatusActive,
		ParentProviderID: &pid,
		QuotaDimension:   provider.QuotaDimensionSpark,
	}
	if err := repo.Create(ctx, shadow); err != nil {
		t.Fatalf("create shadow: %v", err)
	}
	got, err := repo.GetByID(ctx, shadow.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ParentProviderID == nil || *got.ParentProviderID != pid {
		t.Fatalf("ParentProviderID round-trip: %v", got.ParentProviderID)
	}
	if got.QuotaDimension != provider.QuotaDimensionSpark {
		t.Fatalf("QuotaDimension: %q", got.QuotaDimension)
	}
}

func TestListShadowsByParent(t *testing.T) {
	// uq_providers_spark_shadow_per_parent permits one Spark shadow per parent.
	// Use two parents and a regular provider to check that the query returns the requested parent's shadow.
	ctx := context.Background()
	tx := testEntTx(t)
	repo := newProviderStoreContract(tx.Client(), tx, nil)

	parent1 := &provider.Record{
		Name:     "list-parent1",
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Status:   billing.StatusActive,
	}
	if err := repo.Create(ctx, parent1); err != nil {
		t.Fatalf("create parent1: %v", err)
	}
	pid1 := parent1.ID

	shadow1 := &provider.Record{
		Name:             "shadow1",
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		Status:           billing.StatusActive,
		ParentProviderID: &pid1,
		QuotaDimension:   provider.QuotaDimensionSpark,
	}
	if err := repo.Create(ctx, shadow1); err != nil {
		t.Fatalf("create shadow1: %v", err)
	}

	// The other parent's shadow must be excluded.
	parent2 := &provider.Record{
		Name:     "list-parent2",
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Status:   billing.StatusActive,
	}
	if err := repo.Create(ctx, parent2); err != nil {
		t.Fatalf("create parent2: %v", err)
	}
	pid2 := parent2.ID

	shadow2 := &provider.Record{
		Name:             "shadow2",
		Platform:         capability.PlatformOpenAI,
		Type:             capability.ProviderTypeOAuth,
		Status:           billing.StatusActive,
		ParentProviderID: &pid2,
		QuotaDimension:   provider.QuotaDimensionSpark,
	}
	if err := repo.Create(ctx, shadow2); err != nil {
		t.Fatalf("create shadow2: %v", err)
	}

	unrelated := &provider.Record{
		Name:     "unrelated",
		Platform: capability.PlatformOpenAI,
		Type:     capability.ProviderTypeOAuth,
		Status:   billing.StatusActive,
	}
	if err := repo.Create(ctx, unrelated); err != nil {
		t.Fatalf("create unrelated: %v", err)
	}

	got, err := repo.ListShadowsByParent(ctx, pid1)
	if err != nil {
		t.Fatalf("ListShadowsByParent: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 spark shadow for parent1, got %d", len(got))
	}
	acc := got[0]
	if acc.ParentProviderID == nil || *acc.ParentProviderID != pid1 {
		t.Errorf("unexpected ParentProviderID: %v", acc.ParentProviderID)
	}
	if acc.QuotaDimension != provider.QuotaDimensionSpark {
		t.Errorf("unexpected QuotaDimension: %q", acc.QuotaDimension)
	}
	if acc.ID != shadow1.ID {
		t.Errorf("expected shadow1.ID=%d, got %d", shadow1.ID, acc.ID)
	}
}
