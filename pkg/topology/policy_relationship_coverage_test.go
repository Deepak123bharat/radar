package topology

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPolicyRelationshipsIncludeEdgeLessSelectionAndUnknown(t *testing.T) {
	topo := &Topology{Nodes: []Node{
		{ID: "deployment/app/web", Kind: KindDeployment, Name: "web", Data: map[string]any{"namespace": "app", "policySelectionUnknown": true}},
		{ID: "networkpolicy/app/all", Kind: KindNetworkPolicy, Name: "all", Data: map[string]any{"namespace": "app", "matchesAllPods": true}},
		{ID: "networkpolicy/other/all", Kind: KindNetworkPolicy, Name: "all", Data: map[string]any{"namespace": "other", "matchesAllPods": true}},
		{ID: "ciliumclusterwidenetworkpolicy/all", Kind: KindCiliumClusterwideNetworkPolicy, Name: "all", Data: map[string]any{"apiVersion": "cilium.io/v2", "matchesAllPods": true}},
		{ID: "calicoglobalnetworkpolicy/scoped", Kind: KindCalicoGlobalNetworkPolicy, Name: "scoped", Data: map[string]any{"apiVersion": "projectcalico.org/v3", "policyCoverageWorkloads": []string{"deployment/other/db"}}},
	}}
	for _, indexed := range []bool{false, true} {
		var idx *RelationshipsIndex
		if indexed {
			idx = IndexByResource(topo)
		}
		rel := GetRelationshipsWithIndex("deployments", "app", "web", topo, nil, nil, idx)
		if !rel.PolicySelectionUnknown || len(rel.NetworkPolicies) != 2 {
			t.Fatalf("indexed=%v: %+v", indexed, rel)
		}
		for _, p := range rel.NetworkPolicies {
			if p.Namespace == "other" || p.Name == "scoped" {
				t.Fatal("joined unrelated policy", p)
			}
		}
	}
}

func TestStripPolicyEvidenceCopiesSharedData(t *testing.T) {
	original := &Topology{Nodes: []Node{
		{ID: "deployment/app/web", Kind: KindDeployment, Data: map[string]any{"namespace": "app", "policyStatus": "protected", "policySelectionUnknown": true}},
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
	if strings.Contains(string(encoded), "hidden") || strings.Contains(string(encoded), "policyStatus") || strings.Contains(string(encoded), "policySelectionUnknown") {
		t.Fatal(string(encoded))
	}
	if original.Nodes[0].Data["policyStatus"] != "protected" || original.Nodes[0].Data["policySelectionUnknown"] != true || len(original.Nodes[2].Data["policyCoverageWorkloads"].([]string)) != 2 {
		t.Fatal("modified shared cache")
	}
}

func TestUnknownPolicySelectionSurvivesWithoutAnyRelationships(t *testing.T) {
	topo := &Topology{Nodes: []Node{{ID: "deployment/app/web", Kind: KindDeployment, Name: "web", Data: map[string]any{"namespace": "app", "policySelectionUnknown": true}}}}
	rel := GetRelationships("deployments", "app", "web", topo, nil, nil)
	if rel == nil || !rel.PolicySelectionUnknown {
		t.Fatal("empty edges erased uncertainty")
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

func TestUnknownPolicyProvenanceHonorsFilteredViews(t *testing.T) {
	hiddenID := "ciliumclusterwidenetworkpolicy/private"
	original := &Topology{Nodes: []Node{
		{ID: "deployment/app/web", Kind: KindDeployment, Name: "web", policySelectionUnknownSources: []string{hiddenID}, Data: map[string]any{"namespace": "app", "policySelectionUnknown": true, "policyStatus": "unknown"}},
		{ID: hiddenID, Kind: KindCiliumClusterwideNetworkPolicy, Name: "private", Data: map[string]any{"apiVersion": "cilium.io/v2"}},
	}}
	sub := BuildNeighborhood(original, ResourceRef{Kind: "Deployment", Namespace: "app", Name: "web"}, NeighborhoodOptions{Allow: func(n *Node) bool { return n.Kind != KindCiliumClusterwideNetworkPolicy }})
	encoded, err := json.Marshal(sub)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "policySelectionUnknown") || strings.Contains(string(encoded), "policyStatus") {
		t.Fatal(string(encoded))
	}
	filtered := &Topology{Nodes: append([]Node(nil), original.Nodes...)}
	filtered.StripNodeIDs(map[string]bool{hiddenID: true})
	if filtered.Nodes[0].Data["policySelectionUnknown"] != nil {
		t.Fatal("hidden provenance survived strip")
	}
	if original.Nodes[0].Data["policySelectionUnknown"] != true || len(original.Nodes[0].policySelectionUnknownSources) != 1 {
		t.Fatal("mutated shared provenance")
	}
}

func TestPDBDoesNotClaimNetworkPolicySelection(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		workload := Node{ID: "deployment/app/web", Kind: KindDeployment, Data: map[string]any{"namespace": "app", "policySelectionUnknown": unknown}}
		nodes := []Node{workload, {ID: "pdb/app/availability", Kind: KindPDB}}
		annotateNodePolicyCoverage(nodes, []Edge{{Source: "pdb/app/availability", Target: workload.ID, Type: EdgeProtects}}, nil, nil, nil, nil)
		want := "unprotected"
		if unknown {
			want = "unknown"
		}
		if nodes[0].Data["policyStatus"] != want {
			t.Fatalf("PDB claimed policy selection: %v", nodes[0].Data)
		}
	}
}
