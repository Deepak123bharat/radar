package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/mcp"
	"github.com/skyhook-io/radar/internal/search"
	"github.com/skyhook-io/radar/internal/server"
)

// REST and MCP search share one engine but each handler builds its own
// options, so the same query must report the same coverage, spelled the same,
// on both surfaces.
func TestSearchCoverageMatchesAcrossRESTAndMCP(t *testing.T) {
	widgets := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	reports := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "reports"}
	listKinds := map[schema.GroupVersionResource]string{widgets: "WidgetList", reports: "ReportList"}
	resources := []k8s.APIResource{
		{Group: widgets.Group, Version: widgets.Version, Name: widgets.Resource, Kind: "Widget", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		{Group: reports.Group, Version: reports.Version, Name: reports.Resource, Kind: "Report", Namespaced: true, Verbs: []string{"get", "list"}},
	}
	// 25 cluster-scoped kinds Radar's own identity may not list: enough
	// per-kind entries for one reason to reach the broad-query cap.
	var gadgets []schema.GroupVersionResource
	var gadgetTerms []string
	for i := range 25 {
		gvr := schema.GroupVersionResource{Group: "gadgets.example.io", Version: "v1", Resource: fmt.Sprintf("gadget%02ds", i)}
		gadgets = append(gadgets, gvr)
		gadgetTerms = append(gadgetTerms, fmt.Sprintf("kind:Gadget%02d", i))
		listKinds[gvr] = fmt.Sprintf("Gadget%02dList", i)
		resources = append(resources, k8s.APIResource{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: fmt.Sprintf("Gadget%02d", i), Verbs: []string{"get", "list", "watch"}})
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	dyn.PrependReactor("list", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		resource := action.GetResource()
		if resource.Group != "gadgets.example.io" {
			return false, nil, nil
		}
		return true, nil, apierrors.NewForbidden(resource.GroupResource(), "", errors.New("collector cannot list"))
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, resources); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	for _, gvr := range gadgets {
		if err := k8s.GetDynamicResourceCache().EnsureWatching(gvr); err == nil {
			t.Fatalf("%s: collector list should be denied", gvr.Resource)
		}
	}
	srv := server.New(server.Config{DevMode: true, MCPHandler: mcp.NewHandler()})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		srv.Stop()
	})
	session, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "search-parity", Version: "test"}, nil).Connect(context.Background(), &mcpsdk.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	for _, tc := range []struct {
		query string
		want  search.UnsearchedKind
		// named counts the entries for want.Reason that name a kind.
		named int
	}{
		{"robusta", search.UnsearchedKind{Kind: "*", Group: "", Reason: "kind_not_watched"}, 0},
		{"kind:NoSuchKindXyz robusta", search.UnsearchedKind{Kind: "NoSuchKindXyz", Group: "", Reason: "unknown_kind"}, 1},
		{"kind:Report robusta", search.UnsearchedKind{Kind: "Report", Group: "example.io", Reason: "collection_not_supported"}, 1},
		{"robusta", search.UnsearchedKind{Kind: "*", Group: "", Reason: "collector_access_denied"}, 20},
		{strings.Join(gadgetTerms, " ") + " robusta", search.UnsearchedKind{Kind: "Gadget24", Group: "gadgets.example.io", Reason: "collector_access_denied"}, 25},
	} {
		resp, err := http.Get(ts.URL + "/api/search?context=none&include=none&q=" + url.QueryEscape(tc.query))
		if err != nil {
			t.Fatal(err)
		}
		var rest search.Result
		err = json.NewDecoder(resp.Body).Decode(&rest)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: REST search: status %d: %v", tc.query, resp.StatusCode, err)
		}

		result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "search", Arguments: map[string]any{"query": tc.query, "include": "none", "context": "none"}})
		if err != nil || result.IsError || len(result.Content) == 0 {
			t.Fatalf("%s: MCP search failed: %v %+v", tc.query, err, result)
		}
		text, ok := result.Content[0].(*mcpsdk.TextContent)
		if !ok {
			t.Fatalf("%s: MCP search returned %T", tc.query, result.Content[0])
		}
		var viaMCP search.Result
		if err := json.Unmarshal([]byte(text.Text), &viaMCP); err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(rest.Unsearched, viaMCP.Unsearched) || rest.Partial != viaMCP.Partial {
			t.Fatalf("%s: REST coverage %+v differs from MCP %+v", tc.query, rest.Unsearched, viaMCP.Unsearched)
		}
		if !rest.Partial || !slices.ContainsFunc(rest.Unsearched, func(gap search.UnsearchedKind) bool { return reflect.DeepEqual(gap, tc.want) }) {
			t.Fatalf("%s: coverage %+v lacks %+v", tc.query, rest.Unsearched, tc.want)
		}
		named := 0
		for _, gap := range rest.Unsearched {
			if gap.Reason == tc.want.Reason && gap.Kind != "*" {
				named++
			}
		}
		if named != tc.named {
			t.Fatalf("%s: %d named %s entries, want %d: %+v", tc.query, named, tc.want.Reason, tc.named, rest.Unsearched)
		}
	}
}
