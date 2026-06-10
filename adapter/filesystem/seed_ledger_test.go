package filesystem_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
)

func TestSeedLedger_RecordAndRead(t *testing.T) {
	root := t.TempDir()
	l := filesystem.NewSeedLedger(root)

	seeded, err := l.Seeded(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded) != 0 {
		t.Fatalf("fresh ledger = %v", seeded)
	}
	if err := l.Record(context.Background(), "pr-review"); err != nil {
		t.Fatal(err)
	}
	if err := l.Record(context.Background(), "feature-pipeline"); err != nil {
		t.Fatal(err)
	}

	// A fresh ledger over the same root reads both back (persisted to disk).
	seeded, err = filesystem.NewSeedLedger(root).Seeded(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !seeded["pr-review"] || !seeded["feature-pipeline"] {
		t.Errorf("seeded = %v", seeded)
	}
}

func TestSeedLedger_RecordIsIdempotent(t *testing.T) {
	root := t.TempDir()
	l := filesystem.NewSeedLedger(root)
	_ = l.Record(context.Background(), "pr-review")
	_ = l.Record(context.Background(), "pr-review")
	seeded, _ := filesystem.NewSeedLedger(root).Seeded(context.Background())
	if len(seeded) != 1 {
		t.Errorf("seeded = %v, want one entry", seeded)
	}
}
