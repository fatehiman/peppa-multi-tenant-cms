package render

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// Entry is one cached response.
type Entry struct {
	Body        []byte
	ETag        string
	ContentType string
	Status      int
	Modified    time.Time
	expires     time.Time
	key         string
}

// Cache is an LRU page cache with a byte limit, keyed by site key + path.
// Entries also expire after TTL so time-based helpers stay fresh.
type Cache struct {
	Max  int
	TTL  time.Duration
	mu   sync.Mutex
	size int
	ll   *list.List
	m    map[string]*list.Element
}

// NewCache makes a cache of max bytes.
func NewCache(max int) *Cache {
	return &Cache{Max: max, TTL: time.Hour, ll: list.New(), m: map[string]*list.Element{}}
}

func cacheKey(site, path string) string { return site + "\x00" + path }

func entrySize(e *Entry) int { return len(e.Body) + len(e.key) + len(e.ETag) + 128 }

// Get returns a fresh entry or nil.
func (c *Cache) Get(site, path string) *Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	el := c.m[cacheKey(site, path)]
	if el == nil {
		return nil
	}
	e := el.Value.(*Entry)
	if time.Now().After(e.expires) {
		c.remove(el)
		return nil
	}
	c.ll.MoveToFront(el)
	return e
}

// Put stores a body and returns the entry (with its ETag).
func (c *Cache) Put(site, path, contentType string, status int, body []byte) *Entry {
	sum := sha256.Sum256(body)
	e := &Entry{Body: body, ETag: `"` + hex.EncodeToString(sum[:8]) + `"`, ContentType: contentType, Status: status,
		Modified: time.Now().UTC().Truncate(time.Second), expires: time.Now().Add(c.TTL), key: cacheKey(site, path)}
	if entrySize(e) > c.Max/4 {
		return e // too big to keep
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old := c.m[e.key]; old != nil {
		c.remove(old)
	}
	c.m[e.key] = c.ll.PushFront(e)
	c.size += entrySize(e)
	for c.size > c.Max {
		c.remove(c.ll.Back())
	}
	return e
}

func (c *Cache) remove(el *list.Element) {
	e := el.Value.(*Entry)
	c.ll.Remove(el)
	delete(c.m, e.key)
	c.size -= entrySize(e)
}

// Invalidate removes every entry of one site.
func (c *Cache) Invalidate(site string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix := site + "\x00"
	for k, el := range c.m {
		if strings.HasPrefix(k, prefix) {
			c.remove(el)
		}
	}
}

// Clear removes everything.
func (c *Cache) Clear() {
	c.mu.Lock()
	c.ll.Init()
	c.m = map[string]*list.Element{}
	c.size = 0
	c.mu.Unlock()
}

// Size returns the bytes in use and the number of entries.
func (c *Cache) Size() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.size, len(c.m)
}
