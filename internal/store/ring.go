package store

// ring is a fixed-capacity circular buffer. Once full, each push overwrites
// the oldest element, so memory use is constant no matter how long the
// process runs: O(capacity) per check, forever.
type ring[T any] struct {
	buf  []T
	head int // index of the next write
	full bool
}

func newRing[T any](capacity int) *ring[T] {
	return &ring[T]{buf: make([]T, capacity)}
}

func (r *ring[T]) push(v T) {
	r.buf[r.head] = v
	r.head = (r.head + 1) % len(r.buf)
	if r.head == 0 {
		r.full = true
	}
}

func (r *ring[T]) len() int {
	if r.full {
		return len(r.buf)
	}
	return r.head
}

// items returns the contents oldest-first as a new slice.
func (r *ring[T]) items() []T {
	if !r.full {
		return append([]T(nil), r.buf[:r.head]...)
	}
	out := make([]T, 0, len(r.buf))
	out = append(out, r.buf[r.head:]...)
	return append(out, r.buf[:r.head]...)
}
