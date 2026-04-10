package managedagents

import (
	"context"
	"testing"
)

func TestResolveRuntimeRegionIDPrefersConfiguredRegion(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()

	regionID, err := repo.ResolveRuntimeRegionID(ctx, "team_1", "aws-us-east-1")
	if err != nil {
		t.Fatalf("ResolveRuntimeRegionID: %v", err)
	}
	if regionID != "aws-us-east-1" {
		t.Fatalf("regionID = %q, want aws-us-east-1", regionID)
	}
}

func TestResolveRuntimeRegionIDDefaultsWithoutConfiguredRegion(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()

	regionID, err := repo.ResolveRuntimeRegionID(ctx, "team_1", "")
	if err != nil {
		t.Fatalf("ResolveRuntimeRegionID: %v", err)
	}
	if regionID != "default" {
		t.Fatalf("regionID = %q, want default", regionID)
	}
}
