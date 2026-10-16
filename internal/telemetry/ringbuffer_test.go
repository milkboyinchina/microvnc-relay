package telemetry

import (
	"reflect"
	"sync"
	"testing"
)

func TestRingBufferOrderAndWrap(t *testing.T) {
	rb := NewRingBuffer[int](4)

	if rb.Cap() != 4 || rb.Len() != 0 {
		t.Fatalf("expected cap=4, len=0; got cap=%d, len=%d", rb.Cap(), rb.Len())
	}

	// Push 3 items (not wrapped yet)
	rb.Push(10)
	rb.Push(20)
	rb.Push(30)

	want := []int{10, 20, 30}
	if got := rb.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("Snapshot before wrap = %v; want %v", got, want)
	}

	// Push 3 more items (wraps around capacity 4, overwriting 10 and 20)
	rb.Push(40)
	rb.Push(50)
	rb.Push(60)

	if rb.Len() != 4 {
		t.Errorf("Len after wrap = %d; want 4", rb.Len())
	}

	wantWrapped := []int{30, 40, 50, 60}
	if got := rb.Snapshot(); !reflect.DeepEqual(got, wantWrapped) {
		t.Errorf("Snapshot after wrap = %v; want %v", got, wantWrapped)
	}

	// Reset and verify
	rb.Reset()
	if rb.Len() != 0 || len(rb.Snapshot()) != 0 {
		t.Errorf("expected empty buffer after Reset, got len=%d", rb.Len())
	}
}

func TestRingBufferConcurrency(t *testing.T) {
	rb := NewRingBuffer[int](64)
	var wg sync.WaitGroup

	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				rb.Push(offset*1000 + i)
				if i%25 == 0 {
					_ = rb.Snapshot()
				}
			}
		}(g)
	}

	wg.Wait()
	if rb.Len() != 64 {
		t.Errorf("expected Len=64 after concurrent pushes, got %d", rb.Len())
	}
}
