package server

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"sigs.k8s.io/yaml"
)

// integrationActionRoutePrefixes are the route families that act on
// integration custom resources. Helm and node operations have their own chart
// switches; core workload writes are covered by the built-in roles.
var integrationActionRoutePrefixes = []string{
	"/api/argo/", "/api/flux/", "/api/rollouts/", "/api/velero/",
	"/api/gitops/", "/api/cnpg/", "/api/policy/",
}

// TestIntegrationWriteActionsHaveReviewedRows makes a new integration action
// route fail until files/integration-write-baseline.yaml lists it in some
// row's actions. It checks the action inventory only: whether a row names
// every resource and verb the action needs stays a review and live-test job.
func TestIntegrationWriteActionsHaveReviewedRows(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "helm", "radar", "files", "integration-write-baseline.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var policy struct {
		Entries []struct {
			Actions []string `json:"actions"`
		} `json:"entries"`
	}
	if err := yaml.Unmarshal(raw, &policy); err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, entry := range policy.Entries {
		for _, action := range entry.Actions {
			if !strings.HasPrefix(action, "MCP ") {
				listed[action] = true
			}
		}
	}

	var required []string
	srv := New(Config{DevMode: true})
	err = chi.Walk(srv.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if slices.Contains([]string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace, http.MethodConnect}, method) {
			return nil
		}
		action := method + " " + route
		switch {
		case strings.HasPrefix(route, "/api/workloads/"):
			// Workload routes accept Argo Rollouts alongside the built-in kinds.
			required = append(required, action+" kind=rollouts")
		case !slices.ContainsFunc(integrationActionRoutePrefixes, func(prefix string) bool { return strings.HasPrefix(route, prefix) }):
		case strings.Contains(route, "{action}"):
			if !strings.HasPrefix(route, "/api/rollouts/") {
				t.Errorf("%s dispatches on {action}; enumerate its actions here", action)
			}
			for name := range rolloutOperations {
				required = append(required, action+" action="+name)
			}
		default:
			required = append(required, action)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(required) < 20 {
		t.Fatalf("found only %d integration action routes; the router walk is not seeing the API", len(required))
	}
	for _, action := range required {
		if !listed[action] {
			t.Errorf("integration action %q has no row in files/integration-write-baseline.yaml", action)
		}
		delete(listed, action)
	}
	for action := range listed {
		t.Errorf("files/integration-write-baseline.yaml lists %q, which is not a registered integration action route", action)
	}
}
