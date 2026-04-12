package managedagents

import (
	"context"
	"testing"
)

func TestResolveRuntimeRegionIDDefaultsToGlobalRoutingPlaceholder(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()

	regionID, err := repo.ResolveRuntimeRegionID(ctx, "team_1")
	if err != nil {
		t.Fatalf("ResolveRuntimeRegionID: %v", err)
	}
	if regionID != "default" {
		t.Fatalf("regionID = %q, want default", regionID)
	}
}
