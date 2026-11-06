package modelprompts

import (
	"context"
	"path/filepath"
	"testing"

	"transcriber/internal/db"
)

func TestSetGetClearPersists(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sub", "t.db")

	conn, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s := New(conn)
	if got, err := s.Get(ctx, "m1"); err != nil || got != "" {
		t.Fatalf("empty get = %q, %v", got, err)
	}
	if err := s.Set(ctx, "m1", "  Anders, Knut  "); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, "m2", "Bibelen"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, "m2", "Bibelen, Brunstad"); err != nil { // upsert
		t.Fatal(err)
	}
	conn.Close()

	// Reopen: data survives and migrations are idempotent.
	conn, err = db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	s = New(conn)
	all, err := s.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if all["m1"] != "Anders, Knut" || all["m2"] != "Bibelen, Brunstad" || len(all) != 2 {
		t.Fatalf("all = %#v", all)
	}

	if err := s.Set(ctx, "m1", "   "); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, "m1"); got != "" {
		t.Fatalf("m1 after clear = %q", got)
	}
}
