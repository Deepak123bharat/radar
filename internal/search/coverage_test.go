package search

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/filter"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
)

func TestSearchCoverageAndNewKinds(t *testing.T) {
	for _, tk := range typedKinds {
		t.Run(tk.Kind, func(t *testing.T) {
			p := &fakeProvider{}
			res, err := Search(context.Background(), p, Parse("kind:"+tk.Kind), Options{SkipKinds: map[string]bool{tk.Kind: true}})
			if err != nil || !res.Partial || len(res.Unsearched) != 1 || res.Unsearched[0].Reason != "rbac_denied" || res.Searched != 0 {
				t.Fatalf("denial coverage: %+v %v", res, err)
			}
		})
	}
	p := &fakeProvider{typed: map[string][]runtime.Object{"rolebindings": {&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "binding", Namespace: "team-a"}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "worker"}}}}}}
	f, err := filter.CompileObjectFilter(`has(object.subjects) && object.subjects.exists(s, s.name == "worker")`)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Search(context.Background(), p, Parse("kind:RoleBinding"), Options{Filter: f})
	if err != nil || len(res.Hits) != 1 || res.Partial {
		t.Fatalf("RBAC search: %+v %v", res, err)
	}
}

func TestSearchCoverageColdWarmAndUnknown(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	p := &fakeProvider{kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: {State: k8score.DynamicObservationUnwatched}}}
	res, _ := Search(context.Background(), p, Parse(""), Options{})
	if !res.Partial || len(res.Unsearched) != 1 || res.Unsearched[0].Reason != "kind_not_watched" || len(p.warmed) != 0 {
		t.Fatalf("cold: %+v warmed=%v", res, p.warmed)
	}
	res, _ = Search(context.Background(), p, Parse("kind:Widget"), Options{})
	if res.Partial || len(p.warmed) != 1 {
		t.Fatalf("warm: %+v warmed=%v", res, p.warmed)
	}
	res, _ = Search(context.Background(), p, Parse("kind:DoesNotExist"), Options{})
	if !res.Partial || len(res.Unsearched) != 1 || res.Unsearched[0].Reason != "unknown_kind" {
		t.Fatalf("unknown: %+v", res)
	}
}

func TestSearchFilterFailuresAreBoundedAndSecretSafe(t *testing.T) {
	pods := make([]runtime.Object, 25)
	for i := range pods {
		pods[i] = newPod("team-a", fmt.Sprintf("p-%02d", i), "image", nil)
	}
	p := &fakeProvider{typed: map[string][]runtime.Object{"pods": pods}}
	f, _ := filter.CompileObjectFilter(`object.nonexistent == 1`)
	res, _ := Search(context.Background(), p, Parse("kind:Pod"), Options{Filter: f})
	if !res.Partial || res.FilterErrors != 25 || len(res.FilterFailedObjects) != 20 || res.FilterErrorSample == "" {
		t.Fatalf("failed refs: %+v", res)
	}
	p.typed = map[string][]runtime.Object{"secrets": {&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credential", Namespace: "team-a"}, Data: map[string][]byte{"token": []byte("sensitive")}, StringData: map[string]string{"other": "sensitive"}}}}
	f, _ = filter.CompileObjectFilter(`!has(object.data) && !has(object.stringData) && metadata.namespace == object.metadata.namespace`)
	res, _ = Search(context.Background(), p, Parse("kind:Secret"), Options{Filter: f})
	if res.Partial || len(res.Hits) != 1 {
		t.Fatalf("Secret binding: %+v", res)
	}
}

func TestSearchCoverageReadFailuresAndPartialNamespaces(t *testing.T) {
	for _, reason := range []string{"kind_sync_pending", "kind_sync_failed", "kind_not_watched", "collector_scope_limited"} {
		t.Run(reason, func(t *testing.T) {
			p := &fakeProvider{typedReasons: map[string]string{"Pod": reason}, typed: map[string][]runtime.Object{"pods": {newPod("a", "visible", "x", nil)}}}
			res, _ := Search(context.Background(), p, Parse("kind:Pod"), Options{})
			if !res.Partial || len(res.Unsearched) != 1 || string(res.Unsearched[0].Reason) != reason {
				t.Fatalf("read coverage: %+v", res)
			}
			if reason != "collector_scope_limited" && len(res.Hits) != 0 {
				t.Fatalf("incomplete cache served rows: %+v", res)
			}
		})
	}
	for _, tc := range []struct {
		err    error
		reason string
	}{
		{fmt.Errorf("forbidden: pods"), "collector_access_denied"},
		{apierrors.NewUnauthorized("token expired"), "collector_access_denied"},
		{fmt.Errorf("broken list"), "read_error"},
	} {
		p := &fakeProvider{listErrors: map[string]error{"pods": tc.err}}
		res, _ := Search(context.Background(), p, Parse("kind:Pod"), Options{})
		if !res.Partial || len(res.Unsearched) != 1 || string(res.Unsearched[0].Reason) != tc.reason {
			t.Fatalf("list failure: %+v", res)
		}
	}
	rec := &recordingProvider{fakeProvider: &fakeProvider{}}
	res, _ := Search(context.Background(), rec, Parse("kind:Role"), Options{Namespaces: []string{"a", "b"}, NamespacesByKind: map[string][]string{"Role": {"a"}}})
	if !res.Partial || len(res.Unsearched) != 1 || res.Unsearched[0].Reason != "rbac_denied" || len(rec.listedWith["roles"]) != 1 || rec.listedWith["roles"][0] != "a" {
		t.Fatalf("namespace narrowing: %+v lists=%v", res, rec.listedWith)
	}
	res, _ = Search(context.Background(), rec, Parse("kind:Role"), Options{NamespacesByKind: map[string][]string{"Role": {}}})
	if !res.Partial || res.Searched != 0 {
		t.Fatalf("empty override: %+v", res)
	}
}

func TestSearchDynamicCoverageDoesNotWarmDeniedAndRetainsGoodNamespaces(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	p := &fakeProvider{kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, namespaced: map[schema.GroupVersionResource]bool{gvr: false}, observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: {State: k8score.DynamicObservationUnwatched}}}
	res, _ := Search(context.Background(), p, Parse("kind:Widget"), Options{CanReadClusterScoped: func(kind, group, resource string) (bool, bool) { return false, true }})
	if !res.Partial || res.Unsearched[0].Reason != "rbac_denied" || len(p.warmed) != 0 || len(p.dynamicListNamespaces) != 0 {
		t.Fatalf("denied kind warmed or listed: %+v %+v", res, p)
	}
	for _, tc := range []struct {
		state  k8score.DynamicObservationState
		reason string
	}{{k8score.DynamicObservationDenied, "collector_access_denied"}, {k8score.DynamicObservationSyncing, "kind_sync_pending"}, {k8score.DynamicObservationUnsupported, "collection_not_supported"}, {k8score.DynamicObservationDeferred, "kind_not_watched"}} {
		p.observations[gvr] = k8score.DynamicResourceObservation{State: tc.state}
		query := ""
		if tc.state == k8score.DynamicObservationUnsupported {
			query = "kind:Widget"
		}
		res, _ = Search(context.Background(), p, Parse(query), Options{})
		if !res.Partial || string(res.Unsearched[0].Reason) != tc.reason {
			t.Fatalf("dynamic state %s: %+v", tc.state, res)
		}
	}
	p.observations[gvr] = k8score.DynamicResourceObservation{State: k8score.DynamicObservationSynced}
	p.namespaced[gvr] = true
	p.dynamicErrors = map[string]error{"a": fmt.Errorf("broken list")}
	res, _ = Search(context.Background(), p, Parse("kind:Widget"), Options{Namespaces: []string{"a", "b"}})
	if !res.Partial || len(p.dynamicListNamespaces) != 2 || res.Unsearched[0].Reason != "read_error" {
		t.Fatalf("dynamic partial list: %+v lists=%v", res, p.dynamicListNamespaces)
	}
}

func TestSearchObjectClusterNamespaceAndGuardedArgo(t *testing.T) {
	p := &fakeProvider{typed: map[string][]runtime.Object{"clusterroles": {&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "reader"}}}}}
	f, _ := filter.CompileObjectFilter(`metadata.namespace == "" && object.metadata.namespace == "" && !("app" in labels)`)
	res, _ := Search(context.Background(), p, Parse("kind:ClusterRole"), Options{Filter: f})
	if res.Partial || len(res.Hits) != 1 {
		t.Fatalf("cluster namespace: %+v", res)
	}
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	p = &fakeProvider{kinds: map[schema.GroupVersionResource]string{gvr: "Application"}, dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {
		{Object: map[string]any{"kind": "Application", "metadata": map[string]any{"name": "single"}, "spec": map[string]any{"source": map[string]any{"repoURL": "https://github.com/one"}}}},
		{Object: map[string]any{"kind": "Application", "metadata": map[string]any{"name": "multi"}, "spec": map[string]any{"sources": []any{map[string]any{"repoURL": "https://github.com/two"}}}}},
	}}}
	f, err := filter.CompileObjectFilter(`kind == "Application" && has(spec.sources) && spec.sources.exists(s, has(s.repoURL) && s.repoURL.contains("github.com"))`)
	if err != nil {
		t.Fatal(err)
	}
	res, _ = Search(context.Background(), p, Parse("kind:Application"), Options{Filter: f})
	if res.Partial || len(res.Hits) != 1 || res.Hits[0].Name != "multi" {
		t.Fatalf("Argo guard: %+v", res)
	}
}

// capFixture serves count cluster-scoped kinds named prefix00, prefix01, …
// Each kind's group sorts in the reverse of its name, so discovery hands
// them to Search last-kind-first and the cap cannot lean on discovery order.
func capFixture(p *fakeProvider, prefix string, count int, state k8score.DynamicObservationState) {
	for i := range count {
		gvr := schema.GroupVersionResource{Group: fmt.Sprintf("g%02d.%s.example.io", count-1-i, strings.ToLower(prefix)), Version: "v1", Resource: fmt.Sprintf("%s%02ds", strings.ToLower(prefix), i)}
		p.kinds[gvr] = fmt.Sprintf("%s%02d", prefix, i)
		p.namespaced[gvr] = false
		p.observations[gvr] = k8score.DynamicResourceObservation{State: state}
	}
}

func newCapProvider() *fakeProvider {
	return &fakeProvider{kinds: map[schema.GroupVersionResource]string{}, namespaced: map[schema.GroupVersionResource]bool{}, observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{}}
}

// capNamed is the named entries capFixture produces for kinds first..last.
func capNamed(prefix string, count, first, last int, reason readreason.Code) []UnsearchedKind {
	var gaps []UnsearchedKind
	for i := first; i <= last; i++ {
		gaps = append(gaps, UnsearchedKind{Kind: fmt.Sprintf("%s%02d", prefix, i), Group: fmt.Sprintf("g%02d.%s.example.io", count-1-i, strings.ToLower(prefix)), Reason: reason})
	}
	return gaps
}

func denyDenied(kind, _, _ string) (bool, bool) {
	return !strings.HasPrefix(kind, "Denied"), true
}

func TestSearchBroadCoverageCapsNamedKindsPerReason(t *testing.T) {
	p := newCapProvider()
	capFixture(p, "Denied", 25, k8score.DynamicObservationSynced)

	res, err := Search(context.Background(), p, Parse("robusta"), Options{CanReadClusterScoped: denyDenied})
	if err != nil {
		t.Fatal(err)
	}
	want := append([]UnsearchedKind{{Kind: "*", Group: "", Reason: readreason.RBACDenied}}, capNamed("Denied", 25, 0, 19, readreason.RBACDenied)...)
	if !res.Partial || len(res.Unsearched) != 21 || !reflect.DeepEqual(res.Unsearched, want) {
		t.Fatalf("broad coverage (%d entries): %+v\nwant %+v", len(res.Unsearched), res.Unsearched, want)
	}
	if first, last := res.Unsearched[1], res.Unsearched[20]; first.Kind != "Denied00" || first.Group != "g24.denied.example.io" || last.Kind != "Denied19" || last.Group != "g05.denied.example.io" {
		t.Fatalf("kept entries are not the first 20 by kind: first %+v last %+v", first, last)
	}

	again, _ := Search(context.Background(), p, Parse("robusta"), Options{CanReadClusterScoped: denyDenied})
	if !reflect.DeepEqual(again.Unsearched, res.Unsearched) {
		t.Fatalf("repeat search changed coverage: %+v", again.Unsearched)
	}

	var terms []string
	for i := range 25 {
		terms = append(terms, fmt.Sprintf("kind:Denied%02d", i))
	}
	res, _ = Search(context.Background(), p, Parse(strings.Join(terms, " ")+" robusta"), Options{CanReadClusterScoped: denyDenied})
	if want := capNamed("Denied", 25, 0, 24, readreason.RBACDenied); len(res.Unsearched) != 25 || !reflect.DeepEqual(res.Unsearched, want) {
		t.Fatalf("kind: query must list every named kind, got %d: %+v", len(res.Unsearched), res.Unsearched)
	}
}

func TestSearchCoverageCapIsPerReasonAndKeepsCollapsedReasons(t *testing.T) {
	p := newCapProvider()
	capFixture(p, "Denied", 25, k8score.DynamicObservationSynced)
	capFixture(p, "Pending", 22, k8score.DynamicObservationSyncing)
	capFixture(p, "Blocked", 3, k8score.DynamicObservationDenied)
	capFixture(p, "Cold", 2, k8score.DynamicObservationUnwatched)

	res, _ := Search(context.Background(), p, Parse("robusta"), Options{CanReadClusterScoped: denyDenied, Namespaces: []string{"team-a"}, ScopedNamespaces: []string{"team-a"}})
	want := []UnsearchedKind{
		{Kind: "*", Reason: readreason.KindNotWatched},
		{Kind: "*", Reason: readreason.KindSyncPending},
		{Kind: "*", Reason: readreason.NamespaceScopeLimited, Namespaces: []string{"team-a"}},
		{Kind: "*", Reason: readreason.RBACDenied},
	}
	want = append(want, capNamed("Blocked", 3, 0, 2, readreason.CollectorAccessDenied)...)
	want = append(want, capNamed("Denied", 25, 0, 19, readreason.RBACDenied)...)
	want = append(want, capNamed("Pending", 22, 0, 19, readreason.KindSyncPending)...)
	if len(res.Unsearched) != 47 || !reflect.DeepEqual(res.Unsearched, want) {
		t.Fatalf("per-reason cap (%d entries): %+v\nwant %+v", len(res.Unsearched), res.Unsearched, want)
	}
}

func TestCapNamedGapsOrdersByKindThenGroup(t *testing.T) {
	gaps := func(order []int) []UnsearchedKind {
		all := []UnsearchedKind{
			{Kind: "Alpha", Group: "a.example.io", Reason: readreason.RBACDenied},
			{Kind: "Alpha", Group: "b.example.io", Reason: readreason.RBACDenied},
			{Kind: "Beta", Group: "", Reason: readreason.RBACDenied},
			{Kind: "Beta", Group: "a.example.io", Reason: readreason.RBACDenied},
			{Kind: "Gamma", Group: "a.example.io", Reason: readreason.RBACDenied},
		}
		out := make([]UnsearchedKind, 0, len(order))
		for _, i := range order {
			out = append(out, all[i])
		}
		return out
	}
	want := []UnsearchedKind{
		{Kind: "*", Reason: readreason.RBACDenied},
		{Kind: "Alpha", Group: "a.example.io", Reason: readreason.RBACDenied},
		{Kind: "Alpha", Group: "b.example.io", Reason: readreason.RBACDenied},
		{Kind: "Beta", Group: "", Reason: readreason.RBACDenied},
	}
	for _, order := range [][]int{{0, 1, 2, 3, 4}, {4, 3, 2, 1, 0}, {3, 1, 4, 0, 2}} {
		res := Result{Unsearched: gaps(order)}
		res.capNamedGaps(3)
		if !reflect.DeepEqual(res.Unsearched, want) || !res.Partial {
			t.Fatalf("input order %v: %+v", order, res.Unsearched)
		}
	}
}
