package policy

import "testing"

func TestMemoryStore_RejectsDuplicatePolicies(t *testing.T) {
	_, err := NewMemoryStore([]Policy{
		{Name: "default", Limit: 1, WindowMs: 1000},
		{Name: "default", Limit: 2, WindowMs: 1000},
	})
	if err == nil {
		t.Fatal("expected duplicate policy error")
	}
}

func TestMemoryStore_DefaultsEmptyAlgorithm(t *testing.T) {
	store, err := NewMemoryStore([]Policy{
		{Name: "default", Limit: 1, WindowMs: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	p, ok := store.Get("default")
	if !ok {
		t.Fatal("expected policy to exist")
	}
	if p.Algorithm != AlgorithmSlidingWindow {
		t.Fatalf("expected default algorithm %q, got %q", AlgorithmSlidingWindow, p.Algorithm)
	}
}
