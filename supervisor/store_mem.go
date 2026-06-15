package supervisor

import "sync"

// MemStore is an in-memory Store for tests (and any future embedded/ephemeral
// use). Concurrency-safe so loop tests can hit it from multiple goroutines.
type MemStore struct {
	mu       sync.Mutex
	policies map[string]Policy
	desired  map[string]DesiredState
	restarts map[string]int
}

var _ Store = (*MemStore)(nil)

func NewMemStore() *MemStore {
	return &MemStore{
		policies: map[string]Policy{},
		desired:  map[string]DesiredState{},
		restarts: map[string]int{},
	}
}

func (m *MemStore) GetPolicy(id string) (Policy, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.policies[id]
	return p, ok, nil
}

func (m *MemStore) SetPolicy(p Policy) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.policies[p.ContainerID] = p
	return nil
}

func (m *MemStore) ListPolicies() ([]Policy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Policy, 0, len(m.policies))
	for _, p := range m.policies {
		out = append(out, p)
	}
	return out, nil
}

func (m *MemStore) GetDesired(id string) (DesiredState, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.desired[id]
	return d, ok, nil
}

func (m *MemStore) SetDesired(id string, d DesiredState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.desired[id] = d
	return nil
}

func (m *MemStore) DeletePolicy(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.policies, id)
	return nil
}

func (m *MemStore) DeleteDesired(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.desired, id)
	return nil
}

func (m *MemStore) BumpRestartCount(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.restarts[id]++
	return nil
}

func (m *MemStore) GetRestartCount(id string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.restarts[id], nil
}

func (m *MemStore) DeleteRestartCount(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.restarts, id)
	return nil
}

func (m *MemStore) Close() error { return nil }
