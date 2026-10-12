package resourcecontext

import (
	"context"
	"encoding/json"
	"github.com/skyhook-io/radar/pkg/topology"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
