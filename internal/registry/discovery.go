package registry

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"
)

type probeKey struct {
	namespace string
	uid       string
	ip        string
	port      int32
}

type portDiscovery struct {
	client *http.Client
	slots  chan struct{}
	mu     sync.Mutex
	cache  map[probeKey]bool
}

func newPortDiscovery(discoveryConcurrency int) *portDiscovery {
	return &portDiscovery{
		client: &http.Client{
			Timeout: time.Second,
			// A PodIP must be contacted directly, never through an environment proxy.
			Transport: &http.Transport{
				DialContext:            (&net.Dialer{Timeout: time.Second, KeepAlive: 30 * time.Second}).DialContext,
				DisableCompression:     true,
				MaxIdleConns:           16,
				MaxIdleConnsPerHost:    2,
				IdleConnTimeout:        30 * time.Second,
				MaxResponseHeaderBytes: 16 << 10,
			},
		},
		slots: make(chan struct{}, discoveryConcurrency),
		cache: make(map[probeKey]bool),
	}
}

// prune also removes replaced/deleted pods; namespace-scoped lists must not
// invalidate entries belonging to other namespaces.
func (d *portDiscovery) prune(namespace string, active map[probeKey]struct{}) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for key := range d.cache {
		_, exists := active[key]
		if (namespace == "" || key.namespace == namespace) && !exists {
			delete(d.cache, key)
		}
	}
}

func (d *portDiscovery) valid(ctx context.Context, key probeKey, address string) bool {
	if ctx.Err() != nil {
		return false
	}
	d.mu.Lock()
	result, exists := d.cache[key]
	d.mu.Unlock()
	if exists {
		return result
	}

	// Shared across all ListHosts calls, not one semaphore per pod/list.
	select {
	case d.slots <- struct{}{}:
		defer func() { <-d.slots }()
	case <-ctx.Done():
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	valid := d.probe(ctx, address)
	// Caller cancellation is not evidence that an endpoint is unavailable.
	if ctx.Err() != nil {
		return false
	}
	d.mu.Lock()
	d.cache[key] = valid
	d.mu.Unlock()
	return valid
}

func (d *portDiscovery) probe(ctx context.Context, address string) bool {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/debug/pprof/", nil)
	if err != nil {
		return false
	}
	response, err := d.client.Do(req)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}
