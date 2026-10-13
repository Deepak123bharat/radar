package configrefs

import (
	"encoding/json"
	"os"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestVPAPodSelectorContractVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/vpa_targets.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name     string         `json:"name"`
		Target   map[string]any `json:"target"`
		Selector *struct {
			APIVersion string `json:"apiVersion"`
			Kind       string `json:"kind"`
			Name       string `json:"name"`
		} `json:"selector"`
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, vector := range vectors {
		t.Run(vector.Name, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"targetRef": vector.Target}}}
			version, kind, name, ok := VPAPodSelectorTarget(obj)
			if vector.Selector == nil {
				if ok {
					t.Fatalf("unexpected selector target: %s %s/%s", version, kind, name)
				}
				return
			}
			if !ok || version != vector.Selector.APIVersion || kind != vector.Selector.Kind || name != vector.Selector.Name {
				t.Fatalf("selector = %s %s/%s (%v), want %+v", version, kind, name, ok, vector.Selector)
			}
		})
	}
}
