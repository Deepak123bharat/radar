package configrefs

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// VPAPodSelectorTarget follows VPA's built-in informer selectors before its
// group-aware scale-subresource lookup. The built-in informers ignore apiVersion.
func VPAPodSelectorTarget(vpa *unstructured.Unstructured) (apiVersion, kind, name string, ok bool) {
	name, _, _ = unstructured.NestedString(vpa.Object, "spec", "targetRef", "name")
	kind, _, _ = unstructured.NestedString(vpa.Object, "spec", "targetRef", "kind")
	if name == "" || kind == "" {
		return "", "", "", false
	}
	apiVersion, _, _ = unstructured.NestedString(vpa.Object, "spec", "targetRef", "apiVersion")
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet":
		apiVersion = "apps/v1"
	case "Job", "CronJob":
		apiVersion = "batch/v1"
	case "ReplicationController":
		apiVersion = "v1"
	default:
		if apiVersion == "" {
			return "", "", "", false
		}
	}
	return apiVersion, kind, name, true
}
