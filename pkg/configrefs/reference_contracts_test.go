package configrefs

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
	for _, tc := range []struct {
		raw, ns, name string
		valid         bool
	}{
		{"tls", "app", "tls", true}, {"certs/tls.pem", "certs", "tls.pem", true},
		{"certs/a/b", "", "", false}, {"/tls", "app", "tls", true}, {"certs/", "", "", false},
		{"", "", "", false}, {"certs/*", "", "", false},
	} {
		ref, valid := ContourSecretReference(tc.raw, "app")
		if valid != tc.valid || valid && (ref.Namespace != tc.ns || ref.Name != tc.name) {
			t.Fatalf("%s: %+v valid=%v", tc.raw, ref, valid)
		}
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
