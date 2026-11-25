package httpx

import (
	"errors"
	"testing"
)

func TestParseRange(t *testing.T) {
	const size = 1000

	cases := []struct {
		name    string
		header  string
		want    Range
		wantErr error
	}{
		{
			name:   "empty header serves the whole file",
			header: "",
			want:   Range{Start: 0, Length: size, Partial: false},
		},
		{
			name:   "explicit start and end",
			header: "bytes=0-99",
			want:   Range{Start: 0, Length: 100, Partial: true},
		},
		{
			name:   "open ended range to end of file",
			header: "bytes=900-",
			want:   Range{Start: 900, Length: 100, Partial: true},
		},
		{
			name:   "suffix range returns the tail",
			header: "bytes=-250",
			want:   Range{Start: 750, Length: 250, Partial: true},
		},
		{
			name:   "suffix larger than file is clamped",
			header: "bytes=-5000",
			want:   Range{Start: 0, Length: size, Partial: true},
		},
		{
			name:   "end beyond file is clamped",
			header: "bytes=500-99999",
			want:   Range{Start: 500, Length: 500, Partial: true},
		},
		{
			name:   "single byte",
			header: "bytes=0-0",
			want:   Range{Start: 0, Length: 1, Partial: true},
		},
		{
			name:    "start past end is unsatisfiable",
			header:  "bytes=1000-1200",
			wantErr: ErrRangeNotSatisfiable,
		},
		{
			name:    "reversed range is unsatisfiable",
			header:  "bytes=500-100",
			wantErr: ErrRangeNotSatisfiable,
		},
		{
			name:    "malformed header is unsatisfiable",
			header:  "bytes=abc-def",
			wantErr: ErrRangeNotSatisfiable,
		},
		{
			name:    "empty spec is unsatisfiable",
			header:  "bytes=-",
			wantErr: ErrRangeNotSatisfiable,
		},
		{
			name:   "multiple ranges are ignored and the whole file is served",
			header: "bytes=0-99,200-299",
			want:   Range{Start: 0, Length: size, Partial: false},
		},
		{
			name:   "unknown unit is ignored",
			header: "items=0-99",
			want:   Range{Start: 0, Length: size, Partial: false},
		},
		{
			name:   "zero sized file serves an empty body",
			header: "bytes=0-10",
			want:   Range{Start: 0, Length: 0, Partial: false},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sizeForCase := int64(size)
			if tc.name == "zero sized file serves an empty body" {
				sizeForCase = 0
			}

			got, err := ParseRange(tc.header, sizeForCase)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected error %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected %+v, got %+v", tc.want, got)
			}
		})
	}
}

func TestRangeEnd(t *testing.T) {
	cases := []struct {
		r    Range
		want int64
	}{
		{Range{Start: 0, Length: 1}, 0},
		{Range{Start: 100, Length: 50}, 149},
		{Range{Start: 0, Length: 0}, 0},
	}
	for _, tc := range cases {
		if got := tc.r.End(); got != tc.want {
			t.Fatalf("Range%+v.End() = %d, want %d", tc.r, got, tc.want)
		}
	}
}

func TestBufferPoolReusesBuffers(t *testing.T) {
	pool := NewBufferPool(32 * 1024)

	buf := pool.Get()
	if len(buf) != 32*1024 {
		t.Fatalf("expected 32768 byte buffer, got %d", len(buf))
	}
	pool.Put(buf)

	again := pool.Get()
	if len(again) != 32*1024 {
		t.Fatalf("expected 32768 byte buffer after reuse, got %d", len(again))
	}
}

func TestBufferPoolRejectsForeignBuffers(t *testing.T) {
	pool := NewBufferPool(32 * 1024)
	// A buffer of the wrong size must not poison the pool.
	pool.Put(make([]byte, 10))
	buf := pool.Get()
	if len(buf) != 32*1024 {
		t.Fatalf("pool returned a wrong sized buffer: %d", len(buf))
	}
}

func TestBufferPoolMinimumSize(t *testing.T) {
	pool := NewBufferPool(1)
	if len(pool.Get()) < 32*1024 {
		t.Fatal("pool must enforce a minimum buffer size")
	}
}
