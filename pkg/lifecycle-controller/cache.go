package controllers

import (
	"sync"

	server "github.com/openshift/operator-framework-olm/pkg/lifecycle-server"
)

// CacheEntry holds cached lifecycle data for a single CatalogSource.
// A nil Data field means the image was pulled but contained no lifecycle data (negative cache).
type CacheEntry struct {
	ImageDigest string
	Data        server.LifecycleIndex
}

// LifecycleCache is a thread-safe in-memory cache of lifecycle data keyed by CatalogSource namespace/name.
type LifecycleCache struct {
	mu    sync.RWMutex
	items map[string]*CacheEntry
}

func NewLifecycleCache() *LifecycleCache {
	return &LifecycleCache{
		items: make(map[string]*CacheEntry),
	}
}

func cacheKey(namespace, name string) string {
	return namespace + "/" + name
}

// Get returns the cached entry for the given CatalogSource. found is false if no entry exists.
func (c *LifecycleCache) Get(namespace, name string) (entry *CacheEntry, found bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.items[cacheKey(namespace, name)]
	return e, ok
}

// Set stores lifecycle data for a CatalogSource. Pass nil data for negative caching.
func (c *LifecycleCache) Set(namespace, name, imageDigest string, data server.LifecycleIndex) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[cacheKey(namespace, name)] = &CacheEntry{
		ImageDigest: imageDigest,
		Data:        data,
	}
}

// Delete removes the cache entry for a CatalogSource.
func (c *LifecycleCache) Delete(namespace, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, cacheKey(namespace, name))
}

// Len returns the number of cached entries.
func (c *LifecycleCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}
