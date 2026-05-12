package realtime

// Package-private test hooks. Lives in a _test.go file so they
// never ship in production binaries. The helpers expose just enough
// internal state for resume_test.go to assert ring contents without
// promoting any of these to the public API.

// NextEventIDTesting returns the last event id assigned by the
// broker. Returns 0 if no events have been published.
func (b *Broker) NextEventIDTesting() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nextID
}

// BufferLenTesting returns the number of events currently held in
// the resume ring.
func (b *Broker) BufferLenTesting() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ringLen
}

// BufferSnapshotTesting returns a copy of the ring buffer in
// oldest→newest order. The returned slice is detached from broker
// state — safe to mutate.
func (b *Broker) BufferSnapshotTesting() []bufferedEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ringLen == 0 {
		return nil
	}
	start := (b.ringPos - b.ringLen + b.ringCap) % b.ringCap
	out := make([]bufferedEvent, b.ringLen)
	for i := 0; i < b.ringLen; i++ {
		out[i] = b.ring[(start+i)%b.ringCap]
	}
	return out
}
