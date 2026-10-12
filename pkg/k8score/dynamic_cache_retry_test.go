package k8score

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

func TestProbeRetryNamespaceOutcomes(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	for _, tc := range []struct {
		name       string
		allowed    bool
		refused    int
		unresolved bool
	}{
		{"OK", true, 2, false}, {"Forbidden", false, 2, false}, {"exhausted", true, 100, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allowed := []string{"a"}
			if tc.allowed {
				allowed = append(allowed, "b")
			}
			d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: rateLimitedLists(gvr, allowed, map[string]int{"b": tc.refused}), NamespaceFallbacks: []string{"a", "b"}, ProbeRetryBackoff: []time.Duration{100 * time.Millisecond, time.Millisecond}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.Stop)
			// Exercise startup's owner, rather than relying on a later read.
			d.WarmupParallel([]schema.GroupVersionResource{gvr}, time.Millisecond)
			initial := d.Observation(gvr)
			if !initial.ProbeRetrying || !slices.Equal(initial.UnansweredNamespaces, []string{"b"}) {
				t.Fatalf("retry not published: %+v", initial)
			}
			if specific := d.NamespaceObservation(gvr, "a"); specific.ProbeRetrying || len(specific.UnansweredNamespaces) > 0 {
				t.Fatalf("a inherited b's gap: %+v", specific)
			}
			d.mu.RLock()
			r := d.probeRetries[gvr]
			d.mu.RUnlock()
			select {
			case <-r.done:
			case <-time.After(3 * time.Second):
				t.Fatal("retry did not finish")
			}
			got := d.Observation(gvr)
			if got.ProbeRetrying || (len(got.UnansweredNamespaces) > 0) != tc.unresolved {
				t.Fatalf("terminal observation: %+v", got)
			}
			if d.HasCoveringInformer(gvr, "b") != (tc.allowed && !tc.unresolved) {
				t.Fatalf("wrong namespace watch: %+v", got)
			}
			if !tc.unresolved && !d.WaitForSync(gvr, time.Second) {
				t.Fatal("watch did not sync")
			}
			if tc.name == "Forbidden" && (got.State == DynamicObservationDenied || got.ReasonCode == "scope_probe_incomplete" || got.Truncated) {
				t.Fatalf("denial did not become scope: %+v", got)
			}
			if tc.unresolved {
				// Repeated reads must not renew the retry budget.
				for range 10 {
					if err := d.EnsureWatching(gvr); err != nil {
						t.Fatal(err)
					}
				}
				d.mu.RLock()
				same := d.probeRetries[gvr] == r && !r.running
				d.mu.RUnlock()
				if !same {
					t.Fatal("exhausted retry restarted")
				}
			}
		})
	}
}

func TestProbeRetryWholeKindOutcomes(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	for _, tc := range []struct {
		name    string
		allowed []string
		refused int
		want    DynamicObservationState
		whole   bool
	}{
		{"OK", []string{""}, 1, DynamicObservationSynced, false},
		{"Forbidden", nil, 1, DynamicObservationDenied, false},
		{"exhausted", nil, 100, DynamicObservationDeferred, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: rateLimitedLists(gvr, tc.allowed, map[string]int{"": tc.refused}), ProbeRetryBackoff: []time.Duration{50 * time.Millisecond}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.Stop)
			if err := d.EnsureWatching(gvr); !IsClientRateLimited(err) {
				t.Fatalf("initial walk: %v", err)
			}
			if !d.Observation(gvr).ProbeRetrying {
				t.Fatal("whole-kind retry not published")
			}
			d.mu.RLock()
			r := d.probeRetries[gvr]
			d.mu.RUnlock()
			select {
			case <-r.done:
			case <-time.After(time.Second):
				t.Fatal("retry did not finish")
			}
			if tc.want == DynamicObservationSynced && !d.WaitForSync(gvr, time.Second) {
				t.Fatal("watch did not sync")
			}
			got := d.Observation(gvr)
			if got.ProbeRetrying || got.State != tc.want || (got.ReasonCode == "scope_probe_incomplete") != tc.whole {
				t.Fatalf("terminal observation: %+v", got)
			}
		})
	}
}

func TestProbeRetryCandidateCapIsOnlySetup(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: rateLimitedLists(gvr, []string{"a"}, nil), NamespaceFallbacks: []string{"a"}, NamespaceFallbacksTruncated: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	if err := d.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !d.WaitForSync(gvr, time.Second) {
		t.Fatal("watch did not sync")
	}
	got := d.Observation(gvr)
	if !got.Truncated || got.ProbeRetrying || len(got.UnansweredNamespaces) > 0 {
		t.Fatalf("cap became unanswered: %+v", got)
	}
	d.mu.RLock()
	r := d.probeRetries[gvr]
	d.mu.RUnlock()
	if r != nil {
		t.Fatal("cap started a retry")
	}
}

func TestProbeRetryOneLoopPerGVR(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: rateLimitedLists(gvr, nil, map[string]int{"b": 100}), ProbeRetryBackoff: []time.Duration{time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	o := probeOutcome{startedAt: time.Now(), unanswered: []string{"b"}}
	d.noteWalk(gvr, o)
	d.mu.RLock()
	r := d.probeRetries[gvr]
	d.mu.RUnlock()
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { d.noteWalk(gvr, o) })
	}
	wg.Wait()
	d.mu.RLock()
	same := d.probeRetries[gvr] == r && r.running
	d.mu.RUnlock()
	if !same {
		t.Fatal("multiple retry owners")
	}
	d.Stop()
	select {
	case <-r.done:
	case <-time.After(time.Second):
		t.Fatal("Stop leaked scheduled retry")
	}
	d.mu.RLock()
	running := r.running
	d.mu.RUnlock()
	if running {
		t.Fatal("Stop left retry running")
	}
}

func TestProbeRetryDeadlineIncludesFirstWalk(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	var probes atomic.Int32
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
	dyn.PrependReactor("list", "widgets", func(ktesting.Action) (bool, runtime.Object, error) {
		probes.Add(1)
		return true, nil, context.DeadlineExceeded
	})
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn, ProbeRetryBackoff: []time.Duration{time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	d.noteWalk(gvr, probeOutcome{startedAt: time.Now().Add(-probeRetryBudget + 50*time.Millisecond), unanswered: []string{"b"}})
	d.mu.RLock()
	r := d.probeRetries[gvr]
	d.mu.RUnlock()
	select {
	case <-r.done:
	case <-time.After(time.Second):
		t.Fatal("retry budget reset after first walk")
	}
	if probes.Load() != 0 || !slices.Equal(d.Observation(gvr).UnansweredNamespaces, []string{"b"}) {
		t.Fatal("deadline lost unanswered check")
	}
}

func TestProbeRetryLaterReadClearsUnanswered(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: rateLimitedLists(gvr, []string{"a", "b"}, map[string]int{"b": 3}), NamespaceFallbacks: []string{"a", "b"}, ProbeRetryBackoff: []time.Duration{time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	if err := d.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	d.mu.RLock()
	r := d.probeRetries[gvr]
	d.mu.RUnlock()
	select {
	case <-r.done:
	case <-time.After(time.Second):
		t.Fatal("retry did not exhaust")
	}
	if !slices.Equal(d.Observation(gvr).UnansweredNamespaces, []string{"b"}) {
		t.Fatal("missing exhausted namespace")
	}
	if err := d.EnsureWatchingContext(t.Context(), gvr, "b"); err != nil {
		t.Fatal(err)
	}
	got := d.Observation(gvr)
	if got.ProbeRetrying || len(got.UnansweredNamespaces) > 0 || !d.HasCoveringInformer(gvr, "b") {
		t.Fatalf("successful read did not settle gap: %+v", got)
	}
}

func TestProbeRetryStopCancelsInFlightRequest(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	entered, canceled := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
	}))
	defer srv.Close()
	client, err := dynamic.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: client, ProbeRetryBackoff: []time.Duration{time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	// Inject an already unanswered walk to isolate the retry request's cancellation.
	d.noteWalk(gvr, probeOutcome{startedAt: time.Now(), unanswered: []string{"b"}})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("retry request not started")
	}
	d.mu.RLock()
	retry := d.probeRetries[gvr]
	d.mu.RUnlock()
	d.Stop()
	select {
	case <-retry.done:
	case <-time.After(time.Second):
		t.Fatal("Stop leaked in-flight retry")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel HTTP request")
	}
}

func TestProbeRetryNoInformerNamespaceIsolation(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: rateLimitedLists(gvr, nil, map[string]int{"b": 100}), NamespaceFallbacks: []string{"a", "b"}, ProbeRetryBackoff: []time.Duration{time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	if err := d.EnsureWatching(gvr); !IsClientRateLimited(err) {
		t.Fatalf("initial walk: %v", err)
	}
	a, b := d.NamespaceObservation(gvr, "a"), d.NamespaceObservation(gvr, "b")
	if a.ProbeRetrying || len(a.UnansweredNamespaces) > 0 || a.ReasonCode == "scope_probe_incomplete" || a.Scope != DynamicObservationScopeExplicitNamespaces {
		t.Fatalf("answered namespace inherited gap: %+v", a)
	}
	if !b.ProbeRetrying || !slices.Equal(b.UnansweredNamespaces, []string{"b"}) {
		t.Fatalf("unanswered namespace lost gap: %+v", b)
	}
}

func TestProbeRetryAllNamespacesForbiddenBecomeScope(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: rateLimitedLists(gvr, nil, map[string]int{"b": 2}), NamespaceFallbacks: []string{"b"}, ProbeRetryBackoff: []time.Duration{time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	if err := d.EnsureWatching(gvr); !IsClientRateLimited(err) {
		t.Fatalf("initial walk: %v", err)
	}
	d.mu.RLock()
	r := d.probeRetries[gvr]
	d.mu.RUnlock()
	select {
	case <-r.done:
	case <-time.After(time.Second):
		t.Fatal("retry did not finish")
	}
	got := d.Observation(gvr)
	if got.ProbeRetrying || len(got.UnansweredNamespaces) > 0 || got.Scope != DynamicObservationScopeExplicitNamespaces || len(got.Namespaces) > 0 {
		t.Fatalf("Forbidden did not become empty collector scope: %+v", got)
	}
}

func TestProbeRetryClusterScopedDenialStaysKindAccess(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	discovery, err := NewResourceDiscovery(fake.NewClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	discovery.AddAPIResource(APIResource{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Widget", Namespaced: false, Verbs: []string{"list", "watch"}})
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: rateLimitedLists(gvr, nil, map[string]int{"": 1}), Discovery: discovery, NamespaceFallbacks: []string{"b"}, NamespaceFallbacksTruncated: true, ProbeRetryBackoff: []time.Duration{time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	if err := d.EnsureWatching(gvr); !IsClientRateLimited(err) {
		t.Fatalf("initial walk: %v", err)
	}
	d.mu.RLock()
	r := d.probeRetries[gvr]
	d.mu.RUnlock()
	select {
	case <-r.done:
	case <-time.After(time.Second):
		t.Fatal("retry did not finish")
	}
	got := d.Observation(gvr)
	if got.State != DynamicObservationDenied || got.Scope == DynamicObservationScopeExplicitNamespaces || got.Truncated || got.ProbeRetrying || len(got.UnansweredNamespaces) > 0 {
		t.Fatalf("cluster-scoped denial changed scope: %+v", got)
	}
}
