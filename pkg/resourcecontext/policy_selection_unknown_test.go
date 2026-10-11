package resourcecontext

import (
	"context"
	"github.com/skyhook-io/radar/pkg/topology"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

func TestPolicySelectionUncertaintyReachesAgentContext(t *testing.T) {
	rc := Build(context.Background(), &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "app"}}, Options{Tier: TierBasic, Relationships: &topology.Relationships{PolicySelectionUnknown: true, PolicySelectionUnknownSources: []topology.ResourceRef{{Kind: "CiliumNetworkPolicy", Group: "cilium.io", Namespace: "app", Name: "unknown"}}, NetworkPolicies: []topology.ResourceRef{{Kind: "CiliumNetworkPolicy", Group: "cilium.io", Namespace: "app", Name: "known"}}}, AccessChecker: allowAllChecker{}})
	if !rc.PolicySelectionUnknown || len(rc.SelectedBy) != 1 {
		t.Fatalf("uncertainty or known selection lost: %+v", rc)
	}
}

func TestUnknownSelectionDoesNotRevealUnreadableClusterwidePolicy(t *testing.T) {
	rc := Build(context.Background(), &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "app"}}, Options{Tier: TierBasic, Relationships: &topology.Relationships{PolicySelectionUnknown: true, PolicySelectionUnknownSources: []topology.ResourceRef{{Kind: "CiliumClusterwideNetworkPolicy", Group: "cilium.io", Name: "hidden"}}}, AccessChecker: denyChecker{group: "cilium.io", kind: "CiliumClusterwideNetworkPolicy", namespace: ""}})
	if rc.PolicySelectionUnknown {
		t.Fatal("unreadable clusterwide policy leaked through uncertainty")
	}
	if len(rc.Omitted) == 0 {
		t.Fatal("RBAC omission should explain incomplete selection")
	}
}
