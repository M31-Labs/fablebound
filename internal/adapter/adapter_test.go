package adapter_test

import (
	"context"
	"errors"
	"testing"

	"m31labs.dev/tiller/internal/adapter"
)

// stubAdapter is a minimal adapter used only for registry tests.
type stubAdapter struct{ name string }

func (s *stubAdapter) Name() string        { return s.name }
func (s *stubAdapter) Enforcement() string { return "full" }
func (s *stubAdapter) Prepare(_ context.Context, _ *adapter.DispatchSpec) error {
	return nil
}
func (s *stubAdapter) Run(_ context.Context, _ *adapter.DispatchSpec) (*adapter.Result, error) {
	return &adapter.Result{Status: "completed"}, nil
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := adapter.NewRegistry()

	a := &stubAdapter{name: "test-alpha"}
	b := &stubAdapter{name: "test-beta"}
	r.Register(a)
	r.Register(b)

	got, err := r.Get("test-alpha")
	if err != nil {
		t.Fatalf("Get(%q): unexpected error: %v", "test-alpha", err)
	}
	if got.Name() != "test-alpha" {
		t.Errorf("Get(%q).Name() = %q; want %q", "test-alpha", got.Name(), "test-alpha")
	}

	got2, err := r.Get("test-beta")
	if err != nil {
		t.Fatalf("Get(%q): unexpected error: %v", "test-beta", err)
	}
	if got2.Name() != "test-beta" {
		t.Errorf("Get(%q).Name() = %q; want %q", "test-beta", got2.Name(), "test-beta")
	}
}

func TestRegistry_UnknownName(t *testing.T) {
	r := adapter.NewRegistry()
	r.Register(&stubAdapter{name: "known"})

	_, err := r.Get("unknown-adapter")
	if err == nil {
		t.Fatal("Get(unknown): expected error, got nil")
	}

	var notFound *adapter.ErrNotFound
	if !errors.As(err, &notFound) {
		t.Errorf("Get(unknown): error type = %T; want *adapter.ErrNotFound", err)
	}
	if notFound.Name != "unknown-adapter" {
		t.Errorf("ErrNotFound.Name = %q; want %q", notFound.Name, "unknown-adapter")
	}
}

func TestRegistry_RegisterOverwrite(t *testing.T) {
	// Second Register with the same name replaces the first (last-write-wins).
	r := adapter.NewRegistry()
	r.Register(&stubAdapter{name: "dup"})
	r.Register(&stubAdapter{name: "dup"})

	_, err := r.Get("dup")
	if err != nil {
		t.Fatalf("Get(dup) after double-register: %v", err)
	}
}
