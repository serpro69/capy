package fileio

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

type countedReader struct {
	io.Reader
	bytes int
}

func (r *countedReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytes += n
	return n, err
}

func TestReadAllBound(t *testing.T) {
	t.Parallel()
	for _, size := range []int{0, 63, 64, 65, 1024} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			t.Parallel()
			r := &countedReader{Reader: strings.NewReader(strings.Repeat("x", size))}
			data, err := ReadAll(r, 64)
			if (err != nil) != (size > 64) {
				t.Errorf("size %d: error = %v", size, err)
			}
			want := min(size, 65)
			if r.bytes != want || len(data) != want {
				t.Errorf("read %d, returned %d bytes; want %d", r.bytes, len(data), want)
			}
		})
	}
}

type failedReader struct{ err error }

func (r failedReader) Read(p []byte) (int, error) {
	return copy(p, "abc"), r.err
}

func TestReadAllFailure(t *testing.T) {
	t.Parallel()
	want := errors.New("read failure")
	data, err := ReadAll(failedReader{want}, 64)
	if !errors.Is(err, want) || string(data) != "abc" {
		t.Fatalf("partial read = %q, %v; want abc and original error", data, err)
	}
}

func TestReadAllInvalidLimit(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{-1, 0, int(^uint(0) >> 1)} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			r := &countedReader{Reader: strings.NewReader("must not read")}
			_, err := ReadAll(r, limit)
			if err == nil || r.bytes != 0 {
				t.Fatalf("invalid limit: error = %v, bytes read = %d", err, r.bytes)
			}
		})
	}
}

func BenchmarkReadAll(b *testing.B) {
	for _, size := range []int{4096, 2 * 1024 * 1024} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			input := strings.Repeat("x", size)
			b.ReportAllocs()
			b.SetBytes(int64(size))
			for b.Loop() {
				data, err := ReadAll(strings.NewReader(input), 2*1024*1024)
				if err != nil || len(data) != size {
					b.Fatalf("read = %d bytes, %v", len(data), err)
				}
			}
		})
	}
}
