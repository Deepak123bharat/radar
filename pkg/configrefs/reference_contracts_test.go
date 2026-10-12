package configrefs

import (
	"encoding/json"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"os"
	"testing"
)

func TestKnativeReferenceAlternatives(t *testing.T) {
	for _, tc := range []struct {
		version, group, wantGroup string
		valid                     bool
	}{
		{"v1", "", "", true}, {"serving.knative.dev/v1", "", "serving.knative.dev", true},
		{"", "eventing.knative.dev", "eventing.knative.dev", true},
		{"eventing.knative.dev/v1", "eventing.knative.dev", "eventing.knative.dev", true},
		{"v1", "serving.knative.dev", "", false}, {"", "", "", false}, {"group/", "", "", false},
	} {
		ref, valid := KnativeReference(map[string]any{"apiVersion": tc.version, "group": tc.group, "kind": "Service", "name": "web"}, "app")
		if valid != tc.valid || valid && (ref.Group != tc.wantGroup || ref.Namespace != "app") {
			t.Fatalf("%s/%s: %+v valid=%v", tc.version, tc.group, ref, valid)
		}
	}
}

func TestContourSecretNamespaceContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/contour_secret_refs.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Case             string `json:"case"`
		Raw              string `json:"raw"`
		DefaultNamespace string `json:"defaultNamespace"`
		Valid            bool   `json:"valid"`
		Namespace        string `json:"namespace"`
		Name             string `json:"name"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Case, func(t *testing.T) {
			ref, valid := ContourSecretReference(tc.Raw, tc.DefaultNamespace)
			if valid != tc.Valid || valid && (ref.Namespace != tc.Namespace || ref.Name != tc.Name) {
				t.Fatalf("%s: %+v valid=%v", tc.Raw, ref, valid)
			}
		})
	}
}

func TestContourUpstreamValidationNamespaces(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"namespace": "app"}, "spec": map[string]any{"routes": []any{map[string]any{"services": []any{map[string]any{"validation": map[string]any{"caSecret": "tls/http"}}}}}, "tcpproxy": map[string]any{"services": []any{map[string]any{"validation": map[string]any{"caSecret": "tcp"}}}}, "virtualhost": map[string]any{"jwtProviders": []any{map[string]any{"remoteJWKS": map[string]any{"validation": map[string]any{"caSecret": "tls/jwks"}}}}}}}}
	refs := ContourSecretReferences(obj)
	if len(refs) != 3 || refs[0].Namespace != "tls" || refs[0].Name != "http" || refs[1].Namespace != "app" || refs[1].Name != "tcp" || refs[2].Namespace != "tls" || refs[2].Name != "jwks" {
		t.Fatalf("refs=%+v", refs)
	}
}
func TestTriggerBrokerLookupIgnoresKReferenceTypeFields(t *testing.T) {
	ref, valid := KnativeTriggerBrokerReference(map[string]any{"namespace": "events", "name": "broker", "kind": "Service", "apiVersion": "v1"})
	if !valid || ref.Group != "eventing.knative.dev" || ref.Kind != "Broker" {
		t.Fatalf("ref=%+v", ref)
	}
	if _, valid := KnativeTriggerBrokerReference(map[string]any{"name": "broker"}); valid {
		t.Fatal("reconciler does not default missing brokerRef namespace")
	}
}
