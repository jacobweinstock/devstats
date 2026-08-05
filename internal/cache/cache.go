// Package cache provides a simple exact-key JSON blob cache. A key maps to a
// single file on disk; repeated fetches for the same key reuse the file.
package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
)

var unsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Cache stores JSON blobs under a directory. A nil or disabled Cache is a
// pass-through: Fetch always calls the underlying function.
type Cache struct {
	dir      string
	disabled bool
}

// New returns a Cache rooted at dir. When disabled, reads and writes are skipped.
func New(dir string, disabled bool) *Cache {
	return &Cache{dir: dir, disabled: disabled}
}

func (c *Cache) path(key string) string {
	return filepath.Join(c.dir, unsafe.ReplaceAllString(key, "_")+".json")
}

// Fetch returns the cached value for key, or calls fn and caches its result.
// A cache miss, unreadable file, or unmarshal error transparently falls back
// to fn.
func Fetch[T any](c *Cache, key string, fn func() (T, error)) (T, error) {
	if c == nil || c.disabled {
		return fn()
	}
	p := c.path(key)
	if b, err := os.ReadFile(p); err == nil {
		var v T
		if json.Unmarshal(b, &v) == nil {
			return v, nil
		}
	}
	v, err := fn()
	if err != nil {
		return v, err
	}
	if b, err := json.Marshal(v); err == nil {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
			_ = os.WriteFile(p, b, 0o644)
		}
	}
	return v, nil
}

// overwrite writes v to key's file, replacing any existing content. It is used
// for volatile (still-open) months that must be refetched on every run, so the
// shard on disk always reflects the latest fetch. A nil or disabled cache is a
// no-op.
func overwrite[T any](c *Cache, key string, v T) {
	if c == nil || c.disabled {
		return
	}
	p := c.path(key)
	if b, err := json.Marshal(v); err == nil {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
			_ = os.WriteFile(p, b, 0o644)
		}
	}
}

// readCached returns the cached slice for key and whether it was a hit. A miss,
// unreadable file, or unmarshal error reports (nil, false). A nil or disabled
// cache is always a miss.
func readCached[T any](c *Cache, key string) ([]T, bool) {
	if c == nil || c.disabled {
		return nil, false
	}
	b, err := os.ReadFile(c.path(key))
	if err != nil {
		return nil, false
	}
	var v []T
	if json.Unmarshal(b, &v) != nil {
		return nil, false
	}
	return v, true
}
