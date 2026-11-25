package transcriber

import (
	"context"
	"testing"
)

type fakeAdapter struct{ id string }

func (f fakeAdapter) ID() string   { return f.id }
func (f fakeAdapter) Name() string { return f.id }
func (f fakeAdapter) Transcribe(context.Context, Request, ProgressFunc) (*Result, error) {
	return nil, nil
}

func TestRegistryIsRemote(t *testing.T) {
	r := NewRegistry("local")
	r.Register(fakeAdapter{"local"})
	r.RegisterRemote(fakeAdapter{"cloud"})
	r.Alias("cloud-alias", "cloud")
	r.Alias("local-alias", "local")

	for id, want := range map[string]bool{
		"local": false, "local-alias": false,
		"cloud": true, "cloud-alias": true,
		"unknown": false,
	} {
		if got := r.IsRemote(id); got != want {
			t.Errorf("IsRemote(%q) = %v, want %v", id, got, want)
		}
	}
	if _, ok := r.Get("cloud"); !ok {
		t.Error("remote adapter not retrievable")
	}
}
