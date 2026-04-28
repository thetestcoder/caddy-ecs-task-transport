package previewrouter

import (
	"time"

	"github.com/jellydator/ttlcache/v3"
)

// hostCache wraps a TTL cache keyed by normalized hostname.
type hostCache struct {
	cache *ttlcache.Cache[string, upstreamTarget]
}

func newHostCache(ttl time.Duration) *hostCache {
	c := ttlcache.New[string, upstreamTarget](
		ttlcache.WithTTL[string, upstreamTarget](ttl),
		ttlcache.WithCapacity[string, upstreamTarget](10000),
	)
	go c.Start() // background eviction loop
	return &hostCache{cache: c}
}

func (c *hostCache) Get(hostname string) (upstreamTarget, bool) {
	item := c.cache.Get(hostname)
	if item == nil {
		return upstreamTarget{}, false
	}
	return item.Value(), true
}

func (c *hostCache) Set(hostname string, target upstreamTarget) {
	c.cache.Set(hostname, target, ttlcache.DefaultTTL)
}

func (c *hostCache) Stop() {
	c.cache.Stop()
}
