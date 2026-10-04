package recall

import (
	"container/list"
	"strings"
	"sync"
)

// lru is a fixed-size cache of query embeddings that evicts the least recently
// used entry. It is safe for concurrent use.
type lru struct {
	mu    sync.Mutex
	max   int
	order *list.List // front = most recently used; values are *lruEntry
	items map[string]*list.Element
}

type lruEntry struct {
	key string
	vec []float32
}

// newLRU creates a cache holding at most size entries (at least 1).
func newLRU(size int) *lru {
	if size < 1 {
		size = 1
	}
	return &lru{max: size, order: list.New(), items: make(map[string]*list.Element)}
}

// get returns the vector stored under key and marks it as recently used.
func (c *lru) get(key string) ([]float32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*lruEntry).vec, true
}

// put stores vec under key, evicting the least recently used entry if full.
func (c *lru) put(key string, vec []float32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value.(*lruEntry).vec = vec
		c.order.MoveToFront(el)
		return
	}
	c.items[key] = c.order.PushFront(&lruEntry{key: key, vec: vec})
	if c.order.Len() > c.max {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.items, last.Value.(*lruEntry).key)
	}
}

// len returns the number of entries.
func (c *lru) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// cacheKey normalizes a query for the cache: lowercase, with runs of
// whitespace collapsed into one space.
func cacheKey(query string) string {
	return strings.Join(strings.Fields(strings.ToLower(query)), " ")
}
