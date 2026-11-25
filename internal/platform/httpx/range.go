// Package httpx contains HTTP plumbing shared by every handler: byte-range
// streaming, buffer pooling and request helpers.
package httpx

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrRangeNotSatisfiable signals that the requested range cannot be served.
var ErrRangeNotSatisfiable = errors.New("httpx: range not satisfiable")

// Range is a resolved byte range.
type Range struct {
	Start   int64
	Length  int64
	Partial bool
}

// End is the inclusive last byte offset.
func (r Range) End() int64 {
	if r.Length <= 0 {
		return r.Start
	}
	return r.Start + r.Length - 1
}

// ParseRange interprets an HTTP Range header against a known content length.
//
// Supported forms: "bytes=start-end", "bytes=start-", "bytes=-suffix".
// A header with multiple ranges is reported as not partial, which tells the
// caller to serve the whole representation: RFC 9110 allows a server to ignore
// the Range header entirely.
func ParseRange(header string, size int64) (Range, error) {
	full := Range{Start: 0, Length: size, Partial: false}
	header = strings.TrimSpace(header)
	if header == "" || size <= 0 {
		return full, nil
	}
	if !strings.HasPrefix(strings.ToLower(header), "bytes=") {
		return full, nil
	}

	spec := strings.TrimSpace(header[len("bytes="):])
	if strings.Contains(spec, ",") {
		return full, nil
	}

	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return Range{}, ErrRangeNotSatisfiable
	}

	startRaw := strings.TrimSpace(parts[0])
	endRaw := strings.TrimSpace(parts[1])

	switch {
	case startRaw == "" && endRaw == "":
		return Range{}, ErrRangeNotSatisfiable

	case startRaw == "":
		// Suffix range: last N bytes.
		suffix, err := strconv.ParseInt(endRaw, 10, 64)
		if err != nil || suffix <= 0 {
			return Range{}, ErrRangeNotSatisfiable
		}
		if suffix > size {
			suffix = size
		}
		return Range{Start: size - suffix, Length: suffix, Partial: true}, nil

	default:
		start, err := strconv.ParseInt(startRaw, 10, 64)
		if err != nil || start < 0 {
			return Range{}, ErrRangeNotSatisfiable
		}
		if start >= size {
			return Range{}, ErrRangeNotSatisfiable
		}
		if endRaw == "" {
			return Range{Start: start, Length: size - start, Partial: true}, nil
		}
		end, err := strconv.ParseInt(endRaw, 10, 64)
		if err != nil || end < start {
			return Range{}, ErrRangeNotSatisfiable
		}
		if end >= size {
			end = size - 1
		}
		return Range{Start: start, Length: end - start + 1, Partial: true}, nil
	}
}

// BufferPool recycles fixed size read buffers so concurrent streaming does not
// allocate per request.
type BufferPool struct {
	size int
	pool sync.Pool
}

// NewBufferPool creates a pool of buffers of the given size.
func NewBufferPool(size int) *BufferPool {
	if size < 32*1024 {
		size = 32 * 1024
	}
	p := &BufferPool{size: size}
	p.pool.New = func() any {
		buf := make([]byte, size)
		return &buf
	}
	return p
}

// Get borrows a buffer.
func (p *BufferPool) Get() []byte { return *p.pool.Get().(*[]byte) }

// Put returns a buffer.
func (p *BufferPool) Put(buf []byte) {
	if cap(buf) != p.size {
		return
	}
	buf = buf[:p.size]
	p.pool.Put(&buf)
}

// Content describes the representation being served.
type Content struct {
	Name        string
	ContentType string
	Size        int64
	ModTime     time.Time
	ETag        string
	// CacheSeconds overrides the default max-age when greater than zero.
	CacheSeconds int
}

// Opener opens the underlying reader. It is called at most once per request.
type Opener func() (io.ReadCloser, error)

// Serve writes a file honouring conditional requests and byte ranges.
//
// It implements the behaviour the TV player depends on: Accept-Ranges, 206
// Partial Content with Content-Range, and 416 for unsatisfiable ranges.
func Serve(w http.ResponseWriter, r *http.Request, c Content, open Opener, pool *BufferPool) error {
	h := w.Header()
	h.Set("Accept-Ranges", "bytes")
	if c.ContentType != "" {
		h.Set("Content-Type", c.ContentType)
	}
	if c.Name != "" {
		h.Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", c.Name))
	}
	if !c.ModTime.IsZero() {
		h.Set("Last-Modified", c.ModTime.UTC().Format(http.TimeFormat))
	}
	if c.ETag != "" {
		h.Set("ETag", c.ETag)
	}

	cacheSeconds := c.CacheSeconds
	if cacheSeconds <= 0 {
		cacheSeconds = 86400
	}
	h.Set("Cache-Control", fmt.Sprintf("public, max-age=%d", cacheSeconds))

	if served, err := checkConditional(w, r, c); err != nil {
		return err
	} else if served {
		return nil
	}

	requested := r.Header.Get("Range")
	// If-Range makes a range request conditional on the validator matching.
	if requested != "" {
		ifRange := strings.TrimSpace(r.Header.Get("If-Range"))
		if ifRange != "" && !validatorMatches(ifRange, c) {
			requested = ""
		}
	}

	rng, err := ParseRange(requested, c.Size)
	if err != nil {
		h.Set("Content-Range", fmt.Sprintf("bytes */%d", c.Size))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return nil
	}

	if !rng.Partial {
		h.Set("Content-Length", strconv.FormatInt(c.Size, 10))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return nil
		}
		w.WriteHeader(http.StatusOK)
		return copyRange(w, open, pool, 0, c.Size)
	}

	h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rng.Start, rng.End(), c.Size))
	h.Set("Content-Length", strconv.FormatInt(rng.Length, 10))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusPartialContent)
		return nil
	}
	w.WriteHeader(http.StatusPartialContent)
	return copyRange(w, open, pool, rng.Start, rng.Length)
}

// ServeBytes is Serve for in-memory payloads such as generated manifests.
func ServeBytes(w http.ResponseWriter, r *http.Request, c Content, body []byte) error {
	c.Size = int64(len(body))
	opener := func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(string(body))), nil
	}
	return Serve(w, r, c, opener, nil)
}

func checkConditional(w http.ResponseWriter, r *http.Request, c Content) (bool, error) {
	etag := c.ETag
	if etag == "" {
		return false, nil
	}

	if inm := r.Header.Get("If-None-Match"); inm != "" {
		if matchETag(inm, etag) {
			w.WriteHeader(http.StatusNotModified)
			return true, nil
		}
		return false, nil
	}

	if im := r.Header.Get("If-Match"); im != "" {
		if !matchETag(im, etag) {
			w.WriteHeader(http.StatusPreconditionFailed)
			return true, nil
		}
	}
	return false, nil
}

func matchETag(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag {
			return true
		}
	}
	return false
}

func validatorMatches(value string, c Content) bool {
	if strings.HasPrefix(value, "W/") || strings.HasPrefix(value, "\"") {
		return c.ETag != "" && value == c.ETag
	}
	if t, err := http.ParseTime(value); err == nil {
		return !c.ModTime.IsZero() && t.Equal(c.ModTime.Truncate(time.Second))
	}
	return false
}

func copyRange(w http.ResponseWriter, open Opener, pool *BufferPool, offset, length int64) error {
	if length <= 0 {
		return nil
	}
	reader, err := open()
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()

	if offset > 0 {
		if seeker, ok := reader.(io.Seeker); ok {
			if _, err := seeker.Seek(offset, io.SeekStart); err != nil {
				return fmt.Errorf("httpx: seek to %d: %w", offset, err)
			}
		} else if _, err := io.CopyN(io.Discard, reader, offset); err != nil {
			return fmt.Errorf("httpx: discard %d bytes: %w", offset, err)
		}
	}

	limited := io.LimitReader(reader, length)

	var buf []byte
	if pool != nil {
		buf = pool.Get()
		defer pool.Put(buf)
	} else {
		buf = make([]byte, 64*1024)
	}

	if _, err := io.CopyBuffer(w, limited, buf); err != nil {
		// A client that seeks or closes mid-stream produces a broken pipe.
		// It is not a server fault, so the error is returned for logging only.
		return fmt.Errorf("httpx: copy body: %w", err)
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}
