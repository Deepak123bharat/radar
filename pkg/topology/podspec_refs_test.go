package topology

import (
	"fmt"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"reflect"
	"testing"
)

func TestWorkloadReferenceExtractionMatchesTypedAndUnstructuredPodSpecs(t *testing.T) {
	backing := []corev1.Container{{Name: "main"}, {Name: "must-not-be-overwritten"}}
	spec := corev1.PodSpec{
		Containers: backing[:1], InitContainers: []corev1.Container{{Name: "init"}},
		EphemeralContainers: []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "debug-config"}}}}}}},
		ImagePullSecrets:    []corev1.LocalObjectReference{{Name: "registry"}},
		Volumes: []corev1.Volume{
			{Name: "projected", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "projected-secret"}}}, {ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "projected-config"}}}}}}},
			{Name: "csi", VolumeSource: corev1.VolumeSource{CSI: &corev1.CSIVolumeSource{Driver: "example.csi", NodePublishSecretRef: &corev1.LocalObjectReference{Name: "driver-credential"}}}},
		},
	}
	typed := extractWorkloadReferences(spec)
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&spec)
	if err != nil {
		t.Fatal(err)
	}
	dynamic, err := extractWorkloadReferencesFromMap(raw)
	if err != nil || !reflect.DeepEqual(typed, dynamic) || !typed.configMaps["debug-config"] || !typed.configMaps["projected-config"] || !typed.secrets["registry"] || !typed.secrets["driver-credential"] || !typed.secrets["projected-secret"] {
		t.Fatalf("typed=%+v dynamic=%+v err=%v", typed, dynamic, err)
	}
	if backing[1].Name != "must-not-be-overwritten" {
		t.Fatal("reference extraction mutated the PodSpec's container backing array")
	}
	if _, err := extractWorkloadReferencesFromMap(map[string]any{"containers": "invalid"}); err == nil {
		t.Fatal("invalid PodSpec silently accepted")
	}
}

func TestResourceGraphSeparatesAdmittedPodFromCurrentAndReplicaTemplates(t *testing.T) {
	makeSpec := func(name string) corev1.PodSpec {
		return corev1.PodSpec{Containers: []corev1.Container{{Name: "app", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}}}}}}}
	}
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: makeSpec("current")}}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "old", Namespace: "team", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "app"}}}, Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: makeSpec("prior")}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "live", Namespace: "team", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "old"}}}, Spec: makeSpec("prior")}
	pod.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "registry"}}
	pod.Spec.EphemeralContainers = []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "debug"}}}}}}}
	provider := &mockProvider{deployments: []*appsv1.Deployment{dep}, replicaSets: []*appsv1.ReplicaSet{rs}, pods: []*corev1.Pod{pod}, configMaps: []*corev1.ConfigMap{{ObjectMeta: metav1.ObjectMeta{Name: "current", Namespace: "team"}}, {ObjectMeta: metav1.ObjectMeta{Name: "prior", Namespace: "team"}}, {ObjectMeta: metav1.ObjectMeta{Name: "debug", Namespace: "team"}}, {ObjectMeta: metav1.ObjectMeta{Name: "debug", Namespace: "other"}}}, secrets: []*corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Name: "registry", Namespace: "team"}}}}
	opts := DefaultBuildOptions()
	opts.IncludeSecrets = true
	opts.IncludeReplicaSets = true
	opts.ForRelationshipCache = true
	topo, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		kind, name string
		obj        runtime.Object
		want       int
	}{{"Deployment", "app", dep, 1}, {"ReplicaSet", "old", rs, 1}, {"Pod", "live", pod, 3}} {
		rel := GetRelationshipsWithObject(test.kind, "team", test.name, test.obj, topo, provider, nil, nil)
		if rel == nil || len(rel.ConfigRefs) != test.want {
			t.Fatalf("%s ConfigRefs=%+v want%d", test.kind, rel, test.want)
		}
		for _, ref := range rel.ConfigRefs {
			if ref.Namespace != "team" {
				t.Fatalf("cross-namespace dependency: %+v", ref)
			}
		}
	}
	reverse := GetRelationships("ConfigMap", "team", "prior", topo, provider, nil)
	if reverse == nil || len(reverse.Consumers) != 2 {
		t.Fatalf("prior ConfigMap consumers=%+v, want live Pod and ReplicaSet", reverse)
	}
}

func TestAdmittedPodFactsDoNotJoinNormalGraphComponents(t *testing.T) {
	provider := &mockProvider{configMaps: []*corev1.ConfigMap{{ObjectMeta: metav1.ObjectMeta{Name: "kube-root-ca.crt", Namespace: "team"}}}, serviceAccounts: []*corev1.ServiceAccount{{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "team"}}}, secrets: []*corev1.Secret{{ObjectMeta: metav1.ObjectMeta{Name: "registry", Namespace: "team"}}}}
	for i, name := range []string{"a", "b", "six"} {
		provider.deployments = append(provider.deployments, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}}}})
		provider.replicaSets = append(provider.replicaSets, &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: name}}}, Spec: appsv1.ReplicaSetSpec{Template: provider.deployments[i].Spec.Template}})
		count := 1
		if name == "six" {
			count = 6
		}
		for j := 0; j < count; j++ {
			config := fmt.Sprintf("%s-%d", name, j)
			provider.configMaps = append(provider.configMaps, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: config, Namespace: "team"}})
			provider.pods = append(provider.pods, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: config, Namespace: "team", Labels: map[string]string{"app": name}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: name}}}, Spec: corev1.PodSpec{ServiceAccountName: "default", ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry"}}, Containers: []corev1.Container{{Name: "app", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: config}}}}}}, Volumes: []corev1.Volume{{Name: "admitted", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "kube-root-ca.crt"}}}}}}}}}})
		}
	}
	opts := DefaultBuildOptions()
	opts.IncludeSecrets = true
	opts.IncludeReplicaSets = true
	normal, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	adjacent := map[string][]string{}
	for _, edge := range normal.Edges {
		adjacent[edge.Source] = append(adjacent[edge.Source], edge.Target)
		adjacent[edge.Target] = append(adjacent[edge.Target], edge.Source)
	}
	visited := map[string]bool{}
	components := 0
	for _, name := range []string{"a", "b", "six"} {
		root := "deployment/team/" + name
		if visited[root] {
			continue
		}
		components++
		queue := []string{root}
		visited[root] = true
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			for _, next := range adjacent[id] {
				if !visited[next] {
					visited[next] = true
					queue = append(queue, next)
				}
			}
		}
	}
	if components != 3 {
		t.Fatalf("normal graph merged unrelated Deployments: %d components", components)
	}
	for _, edge := range normal.Edges {
		if edge.Source == "configmap/team/kube-root-ca.crt" || edge.Source == "serviceaccount/team/default" || edge.Source == "secret/team/registry" {
			t.Fatalf("admitted-only reference enrolled in normal graph: %+v", edge)
		}
	}
	opts.ForRelationshipCache = true
	cached, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, pod := range provider.pods {
		rel := GetRelationshipsWithObject("Pod", "team", pod.Name, pod, cached, provider, nil, nil)
		if rel == nil || len(rel.ConfigRefs) != 3 || rel.ServiceAccount == nil || rel.ServiceAccount.Name != "default" {
			t.Fatalf("individual/grouped Pod lost admitted facts: %s %+v", pod.Name, rel)
		}
		for _, ref := range rel.ConfigRefs {
			if ref.Name != pod.Name && ref.Name != "kube-root-ca.crt" && ref.Name != "registry" {
				t.Fatalf("Pod borrowed sibling configuration: %+v", ref)
			}
		}
	}
	reverse := GetRelationships("ConfigMap", "team", "six-0", cached, provider, nil)
	if reverse == nil || len(reverse.Consumers) != 1 || reverse.Consumers[0].Name != "six-0" {
		t.Fatalf("grouped Pod missing from reverse relationships: %+v", reverse)
	}
	indexed := GetRelationshipsWithObject("ConfigMap", "team", "six-0", provider.configMaps[3], cached, nil, nil, IndexByResource(cached))
	if indexed == nil || len(indexed.Consumers) != 1 || indexed.Consumers[0].Name != "six-0" {
		t.Fatalf("reverse lookup needs raw provider instead of cached facts: %+v", indexed)
	}
	opts.SummaryMode = true
	summarized, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	summaryRel := GetRelationshipsWithObject("Pod", "team", "six-0", provider.pods[2], summarized, provider, nil, nil)
	if summaryRel == nil || len(summaryRel.ConfigRefs) != 3 {
		t.Fatalf("summary Pod lost admitted configuration: %+v", summaryRel)
	}
	filtered := *cached
	filtered.Nodes = append([]Node(nil), cached.Nodes...)
	filtered.Edges = append([]Edge(nil), cached.Edges...)
	filtered.StripNodeKinds(map[NodeKind]bool{KindPod: true, KindPodGroup: true})
	denied := GetRelationships("ConfigMap", "team", "six-0", &filtered, provider, nil)
	if denied != nil {
		for _, consumer := range denied.Consumers {
			if consumer.Kind == "Pod" {
				t.Fatalf("reverse projection bypassed filtered Pod visibility: %+v", consumer)
			}
		}
	}
	cached.StripSecretsExcept(map[SARTuple]bool{})
	rel := GetRelationshipsWithObject("Pod", "team", "six-0", provider.pods[2], cached, provider, nil, nil)
	for _, ref := range rel.ConfigRefs {
		if ref.Kind == "Secret" {
			t.Fatalf("direct projection bypassed Secret filtering: %+v", ref)
		}
	}
}
