package policy

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

const (
	AlgorithmSlidingWindow = "sliding_window"
	AlgorithmGCRA          = "gcra"
)

type Policy struct {
	Name      string `json:"name"`
	Limit     int64  `json:"limit"`
	WindowMs  int64  `json:"window_ms"`
	Algorithm string `json:"algorithm,omitempty"`
}

type fileFormat struct {
	Policies []Policy `json:"policies"`
}

type MemoryStore struct {
	policies map[string]Policy
}

func NewMemoryStore(policies []Policy) (*MemoryStore, error) {
	store := &MemoryStore{policies: make(map[string]Policy, len(policies))}
	for _, p := range policies {
		if err := Validate(p); err != nil {
			return nil, err
		}
		if _, exists := store.policies[p.Name]; exists {
			return nil, fmt.Errorf("duplicate policy %q", p.Name)
		}
		store.policies[p.Name] = normalize(p)
	}
	return store, nil
}

func DefaultStore() *MemoryStore {
	store, err := NewMemoryStore(DefaultPolicies())
	if err != nil {
		panic(err)
	}
	return store
}

func DefaultPolicies() []Policy {
	return []Policy{
		{Name: "default", Limit: 100, WindowMs: 60_000, Algorithm: AlgorithmSlidingWindow},
		{Name: "ip-basic", Limit: 100, WindowMs: 60_000, Algorithm: AlgorithmGCRA},
		{Name: "login", Limit: 5, WindowMs: 300_000, Algorithm: AlgorithmGCRA},
		{Name: "login-route", Limit: 5, WindowMs: 300_000, Algorithm: AlgorithmGCRA},
		{Name: "public-api", Limit: 1_000, WindowMs: 86_400_000, Algorithm: AlgorithmGCRA},
		{Name: "user-free", Limit: 1_000, WindowMs: 86_400_000, Algorithm: AlgorithmGCRA},
	}
}

func LoadFile(path string) (*MemoryStore, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read policy file: %w", err)
	}

	var wrapped fileFormat
	if err := json.Unmarshal(data, &wrapped); err == nil && wrapped.Policies != nil {
		return NewMemoryStore(wrapped.Policies)
	}

	var policies []Policy
	if err := json.Unmarshal(data, &policies); err != nil {
		return nil, fmt.Errorf("decode policy file: %w", err)
	}
	return NewMemoryStore(policies)
}

func (s *MemoryStore) Get(name string) (Policy, bool) {
	if s == nil {
		return Policy{}, false
	}
	p, ok := s.policies[name]
	return p, ok
}

func (s *MemoryStore) List() []Policy {
	if s == nil {
		return nil
	}
	policies := make([]Policy, 0, len(s.policies))
	for _, p := range s.policies {
		policies = append(policies, p)
	}
	sort.Slice(policies, func(i, j int) bool {
		return policies[i].Name < policies[j].Name
	})
	return policies
}

func Validate(p Policy) error {
	if p.Name == "" {
		return fmt.Errorf("policy name is required")
	}
	if p.Limit <= 0 {
		return fmt.Errorf("policy %q limit must be greater than zero", p.Name)
	}
	if p.WindowMs <= 0 {
		return fmt.Errorf("policy %q window_ms must be greater than zero", p.Name)
	}
	switch p.Algorithm {
	case "", AlgorithmSlidingWindow, AlgorithmGCRA:
		return nil
	default:
		return fmt.Errorf("policy %q has unsupported algorithm %q", p.Name, p.Algorithm)
	}
}

func normalize(p Policy) Policy {
	if p.Algorithm == "" {
		p.Algorithm = AlgorithmSlidingWindow
	}
	return p
}
