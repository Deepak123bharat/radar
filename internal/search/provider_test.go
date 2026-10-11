package search

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func TestCacheProviderTypedCoverageUsesReadinessAndScope(t *testing.T) {
	cache, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:         fake.NewClientset(),
		ResourceScopes: map[string]k8score.ResourceScope{"pods": {Enabled: true, Namespace: "team-a"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cache.Stop)
	p := &CacheProvider{cache: &k8s.ResourceCache{ResourceCache: cache}}
	for _, tc := range []struct {
		kind       string
		namespaces []string
		reason     string
	}{
		{"Pod", []string{"team-a"}, ""},
		{"Pod", nil, "collector_scope_limited"},
		{"Pod", []string{"team-b"}, "collector_scope_limited"},
		{"Pod", []string{"team-a", "team-b"}, "collector_scope_limited"},
		{"Secret", []string{"team-a"}, "kind_sync_pending"},
	} {
		if got := p.TypedCoverage(tc.kind, tc.namespaces); string(got) != tc.reason {
			t.Errorf("%s %v: got %q want %q", tc.kind, tc.namespaces, got, tc.reason)
		}
	}
}

func TestCacheProviderNilDynamic(t *testing.T) {
	p := &CacheProvider{discovery: &k8s.ResourceDiscovery{}}
	if _, err := p.DynamicResources(); !errors.Is(err, k8s.ErrDynamicNotReady) {
		t.Fatalf("catalog: %v", err)
	}
	if got := p.DynamicObservation(schema.GroupVersionResource{}); got.State != k8score.DynamicObservationSyncing {
		t.Fatalf("observation: %+v", got)
	}
	if err := p.WarmDynamic(context.Background(), schema.GroupVersionResource{}, ""); !errors.Is(err, k8s.ErrDynamicNotReady) {
		t.Fatalf("warm: %v", err)
	}
}

func TestCacheProviderUnavailableTypedUsesCollectorPermissions(t *testing.T) {
	cache, err := k8score.NewResourceCache(k8score.CacheConfig{Client: fake.NewClientset(), ResourceScopes: map[string]k8score.ResourceScope{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cache.Stop)
	p := &CacheProvider{cache: &k8s.ResourceCache{ResourceCache: cache}, permissions: &k8s.ResourcePermissions{}}
	for _, tk := range typedKinds {
		if got := p.TypedCoverage(tk.Kind, nil); got != "collector_access_denied" {
			t.Errorf("%s: %q", tk.Kind, got)
		}
	}
	p.permissions.Secrets = true
	if got := p.TypedCoverage("Secret", nil); got != "kind_sync_pending" {
		t.Fatalf("allowed but unavailable: %q", got)
	}
}

func TestCacheProviderWarmWaitsForDiscoveryWithoutBlocking(t *testing.T) {
	p := &CacheProvider{dynamic: &k8s.DynamicResourceCache{}}
	start := time.Now()
	err := p.WarmDynamic(context.Background(), schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}, "")
	if !errors.Is(err, k8s.ErrDynamicNotReady) || time.Since(start) > time.Second {
		t.Fatalf("discovery wait: %v, %s", err, time.Since(start))
	}
}

func TestCacheProviderNeverWarmsDirectReadBuiltins(t *testing.T) {
	p := &CacheProvider{dynamic: &k8s.DynamicResourceCache{}}
	for _, gvr := range []schema.GroupVersionResource{
		{Group: "coordination.k8s.io", Version: "v1", Resource: "leases"},
		{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"},
		{Group: "", Version: "v1", Resource: "endpoints"},
	} {
		if got := p.DynamicObservation(gvr); got.State != k8score.DynamicObservationUnsupported {
			t.Errorf("%s observation: %+v", gvr.Resource, got)
		}
		if err := p.WarmDynamic(context.Background(), gvr, ""); listErrorReason(err) != "collection_not_supported" {
			t.Errorf("%s warm: %v", gvr.Resource, err)
		}
	}
	crd := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	if got := dynamicObservationReason(p.DynamicObservation(crd)); got != "kind_not_watched" {
		t.Fatalf("unwatched CRD: %q", got)
	}
}
