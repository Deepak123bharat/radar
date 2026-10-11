package topology

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

func TestObservedInventoryConstrainHintsWithoutLosingDescendants(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		authoritative, managed, expected bool
	}{
		{"inventory includes parent", true, true, true},
		{"inventory excludes parent", true, false, false},
		{"controller without inventory", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			topo := &Topology{Nodes: []Node{
				{ID: "helmrelease/flux/release", Kind: KindHelmRelease, Name: "release", Data: map[string]any{"namespace": "flux", "apiVersion": "helm.toolkit.fluxcd.io/v2", "gitopsManagementAuthoritative": tc.authoritative}},
				{ID: "deployment/app/web", Kind: KindDeployment, Name: "web", Data: map[string]any{"namespace": "app"}},
				{ID: "pod/app/generated", Kind: KindPod, Name: "generated", Data: map[string]any{"namespace": "app"}},
			}, Edges: []Edge{{Source: "deployment/app/web", Target: "pod/app/generated", Type: EdgeManages}}}
			if tc.managed {
				topo.Edges = append(topo.Edges, Edge{Source: "helmrelease/flux/release", Target: "deployment/app/web", Type: EdgeManages})
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "generated", Labels: map[string]string{fluxHelmNameLabel: "release", fluxHelmNSLabel: "flux"}}}
			for _, indexed := range []bool{false, true} {
				var idx *RelationshipsIndex
				if indexed {
					idx = IndexByResource(topo)
				}
				got := SynthesizeManagedBy(pod, "Pod", "app", "generated", topo, nil, idx)
				helm := len(got) > 0 && got[0].Group == fluxHelmGroup
				if helm != tc.expected {
					t.Fatalf("indexed=%v manager=%+v", indexed, got)
				}
			}
		})
	}
}
