package providers

// AddForTest appends a provider to the registry (copy-on-write, under the
// lock). For tests that inject a fake provider; production code registers
// through RegisterOpenAI.
func AddForTest(ps ...Provider) {
	EnsureRegistry()
	registryMu.Lock()
	all = append(append(make([]Provider, 0, len(all)+len(ps)), all...), ps...)
	registryMu.Unlock()
}

// SetForTest replaces the whole registry and returns a func restoring the
// previous one — the snapshot/restore tests use around global registration.
func SetForTest(list []Provider) (restore func()) {
	EnsureRegistry()
	registryMu.Lock()
	prev := all
	all = list
	registryMu.Unlock()
	return func() {
		registryMu.Lock()
		all = prev
		registryMu.Unlock()
	}
}
