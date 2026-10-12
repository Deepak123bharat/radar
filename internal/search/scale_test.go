package search

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type cancelSearchProvider struct {
	*fakeProvider
	cancel context.CancelFunc
	stage  string
	lists  int
}

func (p *cancelSearchProvider) ListTyped(kind string, namespaces []string) ([]runtime.Object, error) {
	if p.stage == "typed" {
		p.lists++
		p.cancel()
	}
	return p.fakeProvider.ListTyped(kind, namespaces)
}
func (p *cancelSearchProvider) DynamicObservation(gvr schema.GroupVersionResource, ns string) k8score.DynamicResourceObservation {
	if p.stage == "kind" {
		p.lists++
		p.cancel()
	}
	return p.fakeProvider.DynamicObservation(gvr, ns)
}
func (p *cancelSearchProvider) ListDynamic(ctx context.Context, gvr schema.GroupVersionResource, ns string) ([]*unstructured.Unstructured, error) {
	if p.stage == "namespace" {
		p.lists++
		p.cancel()
	}
	return p.fakeProvider.ListDynamic(ctx, gvr, ns)
}

func TestSearchStopsOnCancellation(t *testing.T) {
	for _, stage := range []string{"before", "typed", "kind", "namespace"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := &cancelSearchProvider{fakeProvider: &fakeProvider{kinds: map[schema.GroupVersionResource]string{}}, cancel: cancel, stage: stage}
			for i := range 500 {
				p.kinds[schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: fmt.Sprintf("widgets-%03d", i)}] = "Widget"
			}
			namespaces := make([]string, 5000)
			for i := range namespaces {
				namespaces[i] = fmt.Sprintf("ns-%05d", i)
			}
			if stage == "before" {
				cancel()
			}
			_, err := Search(ctx, p, Parse("test"), Options{Namespaces: namespaces})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v", err)
			}
			if p.lists > 1 {
				t.Fatalf("continued work after cancellation: %d calls", p.lists)
			}
		})
	}
}

func TestCoverageDeduplicatesByIdentityAndReason(t *testing.T) {
	c := newCoverage(Parse("kind:Widget"))
	c.kind("Widget", "a.io", readreason.ReadError).RetryWithKind = true
	for range 5000 {
		c.kind("Widget", "a.io", readreason.ReadError)
	}
	c.kind("Widget", "b.io", readreason.ReadError)
	c.kind("Widget", "a.io", readreason.KindSyncFailed)
	c.star(readreason.ReadError)
	c.star(readreason.ReadError)
	got := c.result()
	if len(got) != 4 {
		t.Fatalf("gaps = %+v", got)
	}
	if !c.kind("Widget", "a.io", readreason.ReadError).RetryWithKind {
		t.Fatal("duplicate lost retry guidance")
	}
}

func BenchmarkCoverageScale(b *testing.B) {
	kinds := make([]string, 500)
	for i := range kinds {
		kinds[i] = fmt.Sprintf("Widget%03d", i)
	}
	b.ResetTimer()
	for range b.N {
		c := newCoverage(Parse("test"))
		for _, kind := range kinds {
			for range 5000 {
				c.kind(kind, "example.io", readreason.ReadError)
			}
		}
		c.result()
	}
}

type clusterObservationProvider struct {
	*fakeProvider
	observations, reads int
}

func (p *clusterObservationProvider) DynamicObservation(gvr schema.GroupVersionResource, ns string) k8score.DynamicResourceObservation {
	p.observations++
	return p.fakeProvider.DynamicObservation(gvr, ns)
}
func (p *clusterObservationProvider) ListDynamic(context.Context, schema.GroupVersionResource, string) ([]*unstructured.Unstructured, error) {
	p.reads++
	return nil, nil
}

func TestSearchReusesSyncedClusterWideObservation(t *testing.T) {
	p := &clusterObservationProvider{fakeProvider: &fakeProvider{kinds: map[schema.GroupVersionResource]string{}, observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{}}}
	for i := range 500 {
		gvr := schema.GroupVersionResource{Group: "scale.example.io", Version: "v1", Resource: fmt.Sprintf("widgets-%03d", i)}
		p.kinds[gvr] = "Widget"
		p.fakeProvider.observations[gvr] = k8score.DynamicResourceObservation{State: k8score.DynamicObservationSynced, Scope: k8score.DynamicObservationScopeCluster}
	}
	namespaces := make([]string, 5000)
	for i := range namespaces {
		namespaces[i] = fmt.Sprintf("ns-%05d", i)
	}
	_, err := Search(context.Background(), p, Parse("test"), Options{Namespaces: namespaces})
	if err != nil {
		t.Fatal(err)
	}
	if p.observations != 500 || p.reads != 500*5000 {
		t.Fatalf("observations=%d reads=%d", p.observations, p.reads)
	}
}

func TestSearchClusterWideProbeGapRemainsNamespaceSpecific(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	p := &clusterObservationProvider{fakeProvider: &fakeProvider{
		kinds:        map[schema.GroupVersionResource]string{gvr: "Widget"},
		observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: {State: k8score.DynamicObservationSynced, Scope: k8score.DynamicObservationScopeCluster, UnansweredNamespaces: []string{"b"}, ProbeRetrying: true}},
		namespaceObservations: map[schema.GroupVersionResource]map[string]k8score.DynamicResourceObservation{gvr: {
			"a": {State: k8score.DynamicObservationSynced, Scope: k8score.DynamicObservationScopeCluster},
			"b": {State: k8score.DynamicObservationDeferred, Scope: k8score.DynamicObservationScopeCluster, ReasonCode: "scope_probe_incomplete", UnansweredNamespaces: []string{"b"}, ProbeRetrying: true},
		}},
	}}
	got, err := Search(context.Background(), p, Parse("test"), Options{Namespaces: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.observations != 3 || p.reads != 1 || !got.Partial {
		t.Fatalf("observations=%d reads=%d result=%+v", p.observations, p.reads, got)
	}
}

func TestCoverageNamespaceDeduplicationPreservesSort(t *testing.T) {
	c := newCoverage(Parse("ns:z ns:a"))
	for range 5000 {
		c.namespaces(readreason.CollectorScopeLimited, "z", "a", "ambient")
	}
	got := c.result()
	if len(got) != 1 || len(got[0].Namespaces) != 2 || got[0].Namespaces[0] != "a" || got[0].Namespaces[1] != "z" {
		t.Fatalf("gaps=%+v", got)
	}
}

func TestSearchUnknownKindDeduplicatesCase(t *testing.T) {
	got, err := Search(context.Background(), &fakeProvider{}, Parse("kind:Widget kind:widget"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Unsearched) != 1 || got.Unsearched[0].Reason != readreason.UnknownKind {
		t.Fatalf("gaps=%+v", got.Unsearched)
	}
}
