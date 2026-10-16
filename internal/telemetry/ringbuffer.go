package telemetry

import "sync"

// RingBuffer is a thread-safe, fixed-capacity circular buffer.
// Its backing array is allocated once at creation and never grows,
// guaranteeing O(1) push operations and a bounded memory footprint.
type RingBuffer[T any] struct {
	mu       sync.RWMutex
	items    []T
	capacity int
	head     int // Index where the next item will be written
	count    int // Number of valid items currently stored (<= capacity)
}

// NewRingBuffer allocates a fixed-capacity RingBuffer.
// If capacity <= 0, it defaults to 128.
func NewRingBuffer[T any](capacity int) *RingBuffer[T] {
	if capacity <= 0 {
		capacity = 128
	}
	return &RingBuffer[T]{
		items:    make([]T, capacity),
		capacity: capacity,
	}
}

// Push inserts an item into the ring buffer in O(1) time.
// If the buffer is full, the oldest item is overwritten.
func (rb *RingBuffer[T]) Push(item T) {
	rb.mu.Lock()
	rb.items[rb.head] = item
	rb.head = (rb.head + 1) % rb.capacity
	if rb.count < rb.capacity {
		rb.count++
	}
	rb.mu.Unlock()
}

// Snapshot returns a chronological copy of all items from oldest to newest.
func (rb *RingBuffer[T]) Snapshot() []T {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if rb.count == 0 {
		return []T{}
	}

	out := make([]T, rb.count)
	if rb.count < rb.capacity {
		// Buffer has not wrapped yet; items start at index 0
		copy(out, rb.items[:rb.count])
	} else {
		// Buffer is full and has wrapped; oldest item is at rb.head
		firstPart := rb.capacity - rb.head
		copy(out, rb.items[rb.head:])
		copy(out[firstPart:], rb.items[:rb.head])
	}
	return out
}

// Len returns the current number of stored elements.
func (rb *RingBuffer[T]) Len() int {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.count
}

// Cap returns the fixed maximum capacity of the ring buffer.
func (rb *RingBuffer[T]) Cap() int {
	return rb.capacity
}

// Reset clears all items in the ring buffer without reallocating the backing slice.
func (rb *RingBuffer[T]) Reset() {
	rb.mu.Lock()
	var zero T
	for i := range rb.items {
		rb.items[i] = zero
	}
	rb.head = 0
	rb.count = 0
	rb.mu.Unlock()
}
