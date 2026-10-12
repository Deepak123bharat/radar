package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/search"
	"github.com/skyhook-io/radar/pkg/readreason"
)

func TestSearchNamespacedKindRBAC(t *testing.T) {
	for _, kind := range search.NamespacedSearchKinds {
		t.Run(kind.Kind, func(t *testing.T) {
			username := "search-kind-" + kind.Kind
			ctx := withClusterAdmin(t, username)
			perms := getPermCache().Get(username, nil)
			perms.SetCanI("list", kind.Group, kind.Resource, "", false)
			perms.SetCanI("list", kind.Group, kind.Resource, "a", true)
			perms.SetCanI("list", kind.Group, kind.Resource, "b", false)
			decision, ns := mcpSearchKindRBAC(ctx, nil, kind.Group, kind.Resource)
			if decision != "skip" {
				t.Fatalf("cluster-scope bypass: %s %v", decision, ns)
			}
			decision, ns = mcpSearchKindRBAC(ctx, []string{"a", "b"}, kind.Group, kind.Resource)
			if decision != "override" || len(ns) != 1 || ns[0] != "a" {
				t.Fatalf("kind override: %s %v", decision, ns)
			}
			decision, _ = mcpSearchKindRBAC(ctx, []string{"b"}, kind.Group, kind.Resource)
			if decision != "skip" {
				t.Fatalf("denied namespace bypass: %s", decision)
			}
			decision, _ = mcpSearchKindRBAC(context.Background(), nil, kind.Group, kind.Resource)
			if decision != "" {
				t.Fatalf("no-auth gate: %s", decision)
			}
		})
	}
	t.Cleanup(func() { getPermCache().Invalidate() })
}

func TestHandleSearchExcludedNamespaceCoverage(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "excluded-search", []string{"alpha"})
	for _, tc := range []struct {
		input searchInput
		hits  int
	}{
		{searchInput{Query: "kind:Pod", Namespace: "beta"}, 0},
		{searchInput{Query: "kind:Pod ns:alpha ns:beta"}, 1},
	} {
		body := mcpSearch(t, ctx, tc.input)
		excluded := slices.ContainsFunc(body.Unsearched, func(gap search.UnsearchedKind) bool {
			return gap.Kind == "*" && gap.Reason == "namespace_excluded" && slices.Equal(gap.Namespaces, []string{"beta"})
		})
		if !body.Partial || !excluded || len(body.Hits) != tc.hits || (tc.hits > 0 && body.Hits[0].Namespace != "alpha") {
			t.Fatalf("%+v: scope exclusion: %+v", tc.input, body)
		}
	}
}

func TestSearchKindRBACClusterFirstBoundsSARs(t *testing.T) {
	client, err := kubernetes.NewForConfig(&rest.Config{Host: "https://example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous); getPermCache().Invalidate() })
	ctx := withTestUserPerms(t, "search-count", nil, nil)
	checks := 0
	stubSubjectCanI(t, func(_ context.Context, _ kubernetes.Interface, _ string, _ []string, namespace, group, resource, verb string) (bool, error) {
		checks++
		if namespace != "" {
			t.Errorf("cluster-allowed kind rechecked in %q", namespace)
		}
		return true, nil
	})
	namespaces := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	for _, kind := range search.NamespacedSearchKinds {
		decision, scoped := mcpSearchKindRBAC(ctx, namespaces, kind.Group, kind.Resource)
		if decision != "" || scoped != nil {
			t.Fatalf("allowed kind: %s %v", decision, scoped)
		}
	}
	if checks != 3 {
		t.Fatalf("SAR calls = %d, want 3 sensitive kinds", checks)
	}
}

func TestSearchKindRBACNamespaceCheckFailureRetainsAllowedScope(t *testing.T) {
	client, err := kubernetes.NewForConfig(&rest.Config{Host: "https://example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous); getPermCache().Invalidate() })
	ctx := withTestUserPerms(t, "search-check-error", nil, []string{"a", "b"})
	stubSubjectCanI(t, func(_ context.Context, _ kubernetes.Interface, _ string, _ []string, namespace, group, resource, verb string) (bool, error) {
		if namespace == "b" {
			return false, fmt.Errorf("SAR unavailable")
		}
		return namespace == "a", nil
	})
	decision, scoped := mcpSearchKindRBAC(ctx, []string{"a", "b"}, "", "secrets")
	if decision != "permission_check_failed" || len(scoped) != 1 || scoped[0] != "a" {
		t.Fatalf("SAR failure: %s %v", decision, scoped)
	}
	decision, _ = mcpSearchKindRBAC(ctx, []string{"a"}, "", "unavailable")
	if decision != "" {
		t.Fatalf("fully allowed narrower scope: %s", decision)
	}
}

func TestSearchKindRBACDeniedClusterCountsAndCachesNamespaceChecks(t *testing.T) {
	client, err := kubernetes.NewForConfig(&rest.Config{Host: "https://example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous); getPermCache().Invalidate() })
	namespaces := []string{"a", "b", "c", "denied"}
	ctx := withTestUserPerms(t, "search-denied-counts", nil, namespaces)
	var checks atomic.Int32
	stubSubjectCanI(t, func(_ context.Context, _ kubernetes.Interface, _ string, _ []string, namespace, group, resource, verb string) (bool, error) {
		checks.Add(1)
		return namespace != "" && namespace != "denied", nil
	})
	for range 2 {
		for _, kind := range search.NamespacedSearchKinds {
			decision, scoped := mcpSearchKindRBAC(ctx, namespaces, kind.Group, kind.Resource)
			if decision != "override" || len(scoped) != 3 {
				t.Fatalf("%s denied-cluster scope: %s %v", kind.Kind, decision, scoped)
			}
		}
		if got, want := checks.Load(), int32(len(search.NamespacedSearchKinds)*(len(namespaces)+1)); got != want {
			t.Fatalf("SAR calls = %d, want %d (one cluster check plus visible namespaces per sensitive kind, cached on repeat)", got, want)
		}
	}
}

func mcpSearch(t *testing.T, ctx context.Context, input searchInput) search.Result {
	t.Helper()
	input.Context, input.Include = "none", "none"
	result, _, err := handleSearch(ctx, nil, input)
	if err != nil {
		t.Fatal(err)
	}
	var body search.Result
	if err := json.Unmarshal([]byte(extractText(t, result)), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestHandleSearchClusterScopedForNamespaceRestrictedCaller(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "search-node-reader", []string{"alpha"})
	grantClusterRead(t, "search-node-reader", "/nodes")
	for _, input := range []searchInput{
		{Query: "kind:Node node"},
		{Query: "node"},
		{Query: "kind:Node node", Namespace: "alpha"},
	} {
		body := mcpSearch(t, ctx, input)
		nodes := 0
		for _, hit := range body.Hits {
			if hit.Kind == "Node" {
				nodes++
			}
		}
		if nodes != 2 {
			t.Fatalf("%+v: restricted caller with list nodes got %d nodes: %+v", input, nodes, body)
		}
	}
	body := mcpSearch(t, ctx, searchInput{Query: "node", Namespace: "alpha"})
	for _, hit := range body.Hits {
		if hit.Kind == "Node" {
			t.Fatalf("broad namespace-scoped search returned a Node: %+v", body.Hits)
		}
	}

	ctx = withRestrictedUser(t, "search-no-nodes", []string{"alpha"})
	denyClusterRead(t, "search-no-nodes", "/nodes")
	body = mcpSearch(t, ctx, searchInput{Query: "kind:Node node"})
	if len(body.Hits) != 0 || !slices.ContainsFunc(body.Unsearched, func(gap search.UnsearchedKind) bool {
		return gap.Kind == "Node" && gap.Reason == "rbac_denied"
	}) {
		t.Fatalf("caller without list nodes: %+v", body)
	}
}

func TestHandleSearchNamesExcludedNamespacesOnce(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "search-collapse", []string{"alpha"})
	body := mcpSearch(t, ctx, searchInput{Query: "pod ns:beta ns:gamma"})
	excluded := 0
	for _, gap := range body.Unsearched {
		if gap.Reason == "namespace_excluded" {
			excluded++
			if gap.Kind != "*" || !slices.Equal(gap.Namespaces, []string{"beta", "gamma"}) {
				t.Fatalf("collapsed exclusion: %+v", gap)
			}
		}
	}
	if excluded != 1 || len(body.Hits) != 0 {
		t.Fatalf("broad exclusion entries = %d: %+v", excluded, body)
	}
}

// This fixture has no discovery, so a kind: term naming nothing typed may just
// not be discovered yet: the answer is "still loading", not "no such kind".
func TestHandleSearchWithoutDiscoveryIsPendingNotUnknown(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "search-loading", []string{"alpha"})
	body := mcpSearch(t, ctx, searchInput{Query: "kind:NoSuchKindXyz"})
	want := []search.UnsearchedKind{{Kind: "*", Reason: "kind_sync_pending"}}
	if !body.Partial || !reflect.DeepEqual(body.Unsearched, want) {
		t.Fatalf("coverage before discovery = %+v, want %+v", body.Unsearched, want)
	}
}

// coverageHint rides on every partial result, independently of narrowHint.
// (This fixture has no discovery, so every result here is partial; the REST/MCP
// parity test covers a complete result carrying no hint.)
func TestHandleSearchCoverageHintIsIndependentOfNarrowHint(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	for _, tc := range []struct {
		ctx        context.Context
		input      searchInput
		wantNarrow bool
	}{
		{withRestrictedUser(t, "search-hint", []string{"alpha"}), searchInput{Query: "pod ns:beta"}, false},
		{withClusterAdmin(t, "search-hint-admin"), searchInput{Query: "pod", Limit: 1}, true},
	} {
		input := tc.input
		input.Context, input.Include = "none", "none"
		result, _, err := handleSearch(tc.ctx, nil, input)
		if err != nil {
			t.Fatal(err)
		}
		var body searchResponseMCP
		if err := json.Unmarshal([]byte(extractText(t, result)), &body); err != nil {
			t.Fatal(err)
		}
		if !body.Partial || body.CoverageHint == "" || (body.NarrowHint != "") != tc.wantNarrow {
			t.Fatalf("%+v: partial=%v coverageHint=%q narrowHint=%q", tc.input, body.Partial, body.CoverageHint, body.NarrowHint)
		}
	}
	t.Cleanup(func() { getPermCache().Invalidate() })
}

func TestCoverageHintTemplates(t *testing.T) {
	gap := func(kind, group string, reason readreason.Code) search.UnsearchedKind {
		return search.UnsearchedKind{Kind: kind, Group: group, Reason: reason}
	}
	for _, tc := range []struct {
		name string
		res  search.Result
		want string
	}{
		{"pending fold", search.Result{Unsearched: []search.UnsearchedKind{{Kind: "*", Reason: readreason.KindSyncPending, Count: 37}}},
			"37 kinds were still loading; retry in a few seconds before concluding absence."},
		{"pending catalog", search.Result{Unsearched: []search.UnsearchedKind{{Kind: "*", Reason: readreason.KindSyncPending}}},
			"Some kinds were still loading; retry in a few seconds before concluding absence."},
		{"one failure", search.Result{Unsearched: []search.UnsearchedKind{gap("Certificate", "cert-manager.io", readreason.KindSyncFailed)}},
			"Couldn't read Certificate (cert-manager.io); matches of that kind may be missing."},
		{"failures fold", search.Result{Unsearched: []search.UnsearchedKind{
			{Kind: "*", Reason: readreason.ReadError, Count: 1}, gap("Certificate", "cert-manager.io", readreason.KindSyncFailed), gap("Issuer", "cert-manager.io", readreason.ReadError),
		}}, "Couldn't read 3 kinds (Certificate, Issuer, …); matches of those kinds may be missing."},
		{"rbac", search.Result{Unsearched: []search.UnsearchedKind{gap("Secret", "", readreason.RBACDenied)}},
			"You can't list Secrets in some or all namespaces; this result says nothing about those."},
		{"unknown", search.Result{Unsearched: []search.UnsearchedKind{gap("Deploymnet", "", readreason.UnknownKind)}},
			"No kind named Deploymnet on this cluster; check the spelling."},
		{"unsupported", search.Result{Unsearched: []search.UnsearchedKind{gap("Lease", "coordination.k8s.io", readreason.CollectionNotSupported)}},
			"Leases aren't searchable; use list_resources kind=Lease."},
		{"rbac namespace", search.Result{Unsearched: []search.UnsearchedKind{{Kind: "*", Reason: readreason.NamespaceExcluded, Namespaces: []string{"payments-prd"}}}},
			"Namespace payments-prd wasn't searched: it isn't among the namespaces you can see, or doesn't exist."},
		{"scope namespace", search.Result{Unsearched: []search.UnsearchedKind{{Kind: "*", Reason: readreason.CollectorScopeLimited, Namespaces: []string{"billing"}}}},
			"Namespace billing wasn't searched: Radar here watches only some namespaces."},
		{"check failed", search.Result{Unsearched: []search.UnsearchedKind{gap("Secret", "", readreason.PermissionCheckFailed)}},
			"Radar couldn't check your access to Secret; retry."},
		{"collector", search.Result{Unsearched: []search.UnsearchedKind{gap("Node", "", readreason.CollectorAccessDenied)}},
			"Radar isn't allowed to read Nodes on this cluster; granting you access won't help."},
		{"filter", search.Result{FilterErrors: 143, FilterErrorSample: "no such key: readyReplicas"},
			"143 objects failed the filter (no such key: readyReplicas); guard optional fields with has()."},
		{"ordered and capped", search.Result{FilterErrors: 1, FilterErrorSample: "no such key: x", Unsearched: []search.UnsearchedKind{
			gap("Secret", "", readreason.RBACDenied), {Kind: "*", Reason: readreason.KindSyncPending, Count: 2}, gap("Deploymnet", "", readreason.UnknownKind), gap("Node", "", readreason.CollectorAccessDenied),
		}}, "No kind named Deploymnet on this cluster; check the spelling. 1 object failed the filter (no such key: x); guard optional fields with has(). 2 kinds were still loading; retry in a few seconds before concluding absence. +2 more reasons."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.res.Partial = true
			if got := coverageHint(tc.res); got != tc.want {
				t.Fatalf("coverageHint = %q\nwant           %q", got, tc.want)
			}
		})
	}
}

// Every reason search can report, and any it may report later, yields a
// sentence, so a partial result always carries a hint.
func TestCoverageHintCoversEveryReason(t *testing.T) {
	for _, reason := range []readreason.Code{
		readreason.RBACDenied, readreason.CollectorAccessDenied, readreason.CollectorUnauthorized, readreason.PermissionCheckFailed,
		readreason.KindSyncPending, readreason.KindSyncFailed, readreason.KindNotWatched, readreason.CollectorScopeLimited,
		readreason.UnknownKind, readreason.CollectionNotSupported, readreason.NamespaceExcluded, readreason.NamespaceScopeLimited,
		readreason.ReadError, "reason_from_the_future",
	} {
		res := search.Result{Partial: true, Unsearched: []search.UnsearchedKind{{Kind: "Widget", Group: "example.io", Reason: reason, Namespaces: []string{"a"}}}}
		if hint := coverageHint(res); hint == "" || strings.Contains(hint, "%!") {
			t.Fatalf("%s: hint %q", reason, hint)
		}
	}
}
