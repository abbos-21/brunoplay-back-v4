package settings_test

import (
	"context"
	"testing"

	"brunoplay/internal/apperr"
	"brunoplay/internal/settings"
	"brunoplay/internal/testutil"
)

func TestUnconfiguredAndDefaults(t *testing.T) {
	pool := testutil.DB(t)
	s := settings.New(pool)
	s.SetTTL(0)
	ctx := context.Background()

	if _, err := s.Int(ctx, settings.KeyWdMin); !apperr.Is(err, apperr.ConfigMissing) {
		t.Fatalf("a setting without default must be CONFIG_MISSING, got %v", err)
	}
	if v, err := s.Int(ctx, settings.KeyCollectMinPct); err != nil || v != 10 {
		t.Fatalf("default 10%% expected, got %d %v", v, err)
	}
	if v, err := s.Int(ctx, settings.KeyBoosterDays); err != nil || v != 30 {
		t.Fatalf("default 30 days expected, got %d %v", v, err)
	}
	miss, err := s.MissingKeys(ctx)
	if err != nil || len(miss) == 0 {
		t.Fatalf("missing keys expected, got %v %v", miss, err)
	}
}

func TestSetValidatesAndRecordsHistory(t *testing.T) {
	pool := testutil.DB(t)
	s := settings.New(pool)
	s.SetTTL(0)
	ctx := context.Background()

	if err := s.Set(ctx, settings.KeyWdMin, 10000, 0); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Int(ctx, settings.KeyWdMin); v != 10000 {
		t.Fatalf("got %d", v)
	}
	if err := s.Set(ctx, settings.KeyWdMin, 12000, 0); err != nil {
		t.Fatal(err)
	}
	h, err := s.History(ctx, settings.KeyWdMin, 10)
	if err != nil || len(h) != 2 {
		t.Fatalf("history rows %d %v", len(h), err)
	}
	if string(h[0].Old) != "10000" || string(h[0].New) != "12000" {
		t.Fatalf("history order/content wrong: %s -> %s", h[0].Old, h[0].New)
	}

	bad := []struct {
		key string
		val any
	}{
		{settings.KeyWdMin, -5},              // below minimum
		{settings.KeyWdMin, 1.5},             // not whole
		{settings.KeyWdMin, "ten"},           // wrong type
		{settings.KeyCollectMinPct, 101},     // above maximum
		{settings.KeyAdsRequired, "yes"},     // wrong type
		{settings.KeyCardsMining, "no list"}, // json list required
		{settings.KeyAdsBlockIDs, []int{1}},  // json object required
		{"nope.key", 1},                      // unknown key
	}
	for _, b := range bad {
		if err := s.Set(ctx, b.key, b.val, 0); err == nil {
			t.Errorf("%s=%v must be rejected", b.key, b.val)
		}
	}
	if v, _ := s.Int(ctx, settings.KeyWdMin); v != 12000 {
		t.Fatalf("rejected writes must not change the value, got %d", v)
	}
}

func TestSnapshotCacheRefreshes(t *testing.T) {
	pool := testutil.DB(t)
	s := settings.New(pool)
	ctx := context.Background()
	_ = s.Set(ctx, settings.KeyLinkSupport, "https://t.me/a", 0)
	if v, _ := s.Str(ctx, settings.KeyLinkSupport); v != "https://t.me/a" {
		t.Fatal(v)
	}
	// a write through another instance becomes visible once the snapshot is refreshed
	other := settings.New(pool)
	_ = other.Set(ctx, settings.KeyLinkSupport, "https://t.me/b", 0)
	s.SetTTL(0)
	if v, _ := s.Str(ctx, settings.KeyLinkSupport); v != "https://t.me/b" {
		t.Fatalf("snapshot did not refresh: %s", v)
	}
}

func TestRegistryHasUniqueKeys(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range settings.Registry() {
		if seen[d.Key] {
			t.Errorf("duplicate key %s", d.Key)
		}
		seen[d.Key] = true
		if d.Default != nil {
			// a default must itself pass validation
			s := settings.New(nil)
			if _, err := s.Validate(d.Key, d.Default); err != nil {
				t.Errorf("default of %s is invalid: %v", d.Key, err)
			}
		}
	}
}
