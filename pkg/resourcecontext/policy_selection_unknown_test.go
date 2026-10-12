package resourcecontext

import (
	"context"
	"encoding/json"
	"github.com/skyhook-io/radar/pkg/topology"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"strings"
	"testing"
)

func TestPolicySelectionUncertaintyReachesAgentContext(t *testing.T) {
	rc := Build(context.Background(), &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "app"}}, Options{Tier: TierBasic, Relationships: &topology.Relationships{NetworkPolicyMatchesIncomplete: true, NetworkPolicyMatchesIncompleteSources: []topology.ResourceRef{{Kind: "CiliumNetworkPolicy", Group: "cilium.io", Namespace: "app", Name: "unknown"}}, NetworkPolicies: []topology.ResourceRef{{Kind: "CiliumNetworkPolicy", Group: "cilium.io", Namespace: "app", Name: "known"}}}, AccessChecker: allowAllChecker{}})
	if !rc.NetworkPolicyMatchesIncomplete || len(rc.SelectedBy) != 1 {
		t.Fatalf("uncertainty or known selection lost: %+v", rc)
	}
	wire, err := json.Marshal(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wire), `"networkPolicyMatchesIncomplete":true`) || strings.Contains(string(wire), `"policySelectionUnknown"`) || strings.Contains(string(wire), `"name":"unknown"`) {
		t.Fatalf("ambiguous field or private provenance in agent context: %s", wire)
	}
}

func TestUnknownSelectionDoesNotRevealUnreadableClusterwidePolicy(t *testing.T) {
	rc := Build(context.Background(), &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "app"}}, Options{Tier: TierBasic, Relationships: &topology.Relationships{NetworkPolicyMatchesIncomplete: true, NetworkPolicyMatchesIncompleteSources: []topology.ResourceRef{{Kind: "CiliumClusterwideNetworkPolicy", Group: "cilium.io", Name: "hidden"}}}, AccessChecker: denyChecker{group: "cilium.io", kind: "CiliumClusterwideNetworkPolicy", namespace: ""}})
	if rc.NetworkPolicyMatchesIncomplete {
		t.Fatal("unreadable clusterwide policy leaked through uncertainty")
	}
	if len(rc.Omitted) == 0 {
		t.Fatal("RBAC omission should explain incomplete selection")
	}
	wire, err := json.Marshal(rc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "networkPolicyMatchesIncomplete") || strings.Contains(string(wire), "hidden") || !hasOmitted(rc.Omitted, "selectedBy") {
		t.Fatalf("unreadable policy was exposed or omission was lost: %s", wire)
	}
}

func TestAllPodPolicyTargetsReachAgentContextWithPermissions(t *testing.T) {
	policy := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": map[string]any{"name": "all", "namespace": "app"}}}
	graph := &topology.Topology{Nodes: []topology.Node{
		{ID: "networkpolicy/app/all", Kind: topology.KindNetworkPolicy, Name: "all", Data: map[string]any{"namespace": "app", "apiVersion": "networking.k8s.io/v1", "matchesAllPods": true}},
		{ID: "deployment/app/web", Kind: topology.KindDeployment, Name: "web", Data: map[string]any{"namespace": "app", "apiVersion": "apps/v1"}},
	}}
	rel := topology.GetRelationshipsWithObject("NetworkPolicy", "app", "all", policy, graph, nil, nil, nil)
	full := Build(context.Background(), policy, Options{Relationships: rel, AccessChecker: allowAllChecker{}})
	if len(full.Protects) != 1 || full.Protects[0].Kind != "Deployment" {
		t.Fatalf("lost broad policy targets: %+v", full)
	}
	limited := Build(context.Background(), policy, Options{Relationships: rel, AccessChecker: denyChecker{group: "apps", kind: "Deployment", namespace: "app"}})
	if len(limited.Protects) != 0 || !hasOmitted(limited.Omitted, "protects") {
		t.Fatalf("policy targets ignored permissions: %+v", limited)
	}
}
