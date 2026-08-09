package store

// EnableRealRuntime configures the Memory store to use actual Docker containers
// instead of simulated operations. This must be called before any server operations.
func (m *Memory) EnableRealRuntime() error {
	adapter, err := NewRuntimeAdapter(m.fileRoot)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.runtimeAdapter = adapter
	m.mu.Unlock()
	return nil
}

// DisableRealRuntime reverts to simulated operations (for testing).
func (m *Memory) DisableRealRuntime() {
	m.mu.Lock()
	if m.runtimeAdapter != nil {
		m.runtimeAdapter.Close()
		m.runtimeAdapter = nil
	}
	m.mu.Unlock()
}

// UseRealRuntime returns true if real Docker operations are enabled.
func (m *Memory) UseRealRuntime() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.runtimeAdapter != nil
}
