package topology

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStripPolicyEvidenceCopiesSharedData(t *testing.T) {
	original := &Topology{Nodes: []Node{
		{ID: "deployment/app/web", Kind: KindDeployment, Data: map[string]any{"namespace": "app", "policyStatus": "protected"}},
		{ID: "deployment/hidden/db", Kind: KindDeployment, Data: map[string]any{"namespace": "hidden"}},
		{ID: "calicoglobalnetworkpolicy/all", Kind: KindCalicoGlobalNetworkPolicy, Data: map[string]any{"policyCoverageWorkloads": []string{"deployment/app/web", "deployment/hidden/db"}}},
		{ID: "ciliumnetworkpolicy/app/hidden", Kind: KindCiliumNetworkPolicy, Data: map[string]any{"namespace": "app"}},
	}}
	filtered := &Topology{Nodes: append([]Node(nil), original.Nodes...)}
	filtered.StripNodeIDs(map[string]bool{"deployment/hidden/db": true, "ciliumnetworkpolicy/app/hidden": true})
	encoded, err := json.Marshal(filtered)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "hidden") || strings.Contains(string(encoded), "policyStatus") {
		t.Fatal(string(encoded))
	}
	if original.Nodes[0].Data["policyStatus"] != "protected" || len(original.Nodes[2].Data["policyCoverageWorkloads"].([]string)) != 2 {
		t.Fatal("modified shared cache")
	}
}

func TestAuthorizedNeighborhoodDoesNotExposeInternalCoverageIDs(t *testing.T) {
	topo := &Topology{Nodes: []Node{{ID: "globalnetworkpolicy//all", Kind: KindCalicoGlobalNetworkPolicy, Name: "all", Data: map[string]any{"apiVersion": "projectcalico.org/v3", "policyCoverageWorkloads": []string{"deployment/private/secret-workload"}}}}}
	sub := BuildNeighborhood(topo, ResourceRef{Kind: "GlobalNetworkPolicy", Name: "all", Group: "projectcalico.org"}, NeighborhoodOptions{Allow: func(*Node) bool { return true }})
	if len(sub.Nodes) != 1 || sub.Nodes[0].Data["policyCoverageWorkloads"] != nil {
		t.Fatalf("internal IDs escaped: %+v", sub)
	}
	if topo.Nodes[0].Data["policyCoverageWorkloads"] == nil {
		t.Fatal("mutated cached metadata")
	}
}
