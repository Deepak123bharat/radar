package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

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
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{widgets: "WidgetList", reports: "ReportList"})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: widgets.Group, Version: widgets.Version, Name: widgets.Resource, Kind: "Widget", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		{Group: reports.Group, Version: reports.Version, Name: reports.Resource, Kind: "Report", Namespaced: true, Verbs: []string{"get", "list"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
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
	}{
		{"robusta", search.UnsearchedKind{Kind: "*", Group: "", Reason: "kind_not_watched"}},
		{"kind:NoSuchKindXyz robusta", search.UnsearchedKind{Kind: "NoSuchKindXyz", Group: "", Reason: "unknown_kind"}},
		{"kind:Report robusta", search.UnsearchedKind{Kind: "Report", Group: "example.io", Reason: "collection_not_supported"}},
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
	}
}
