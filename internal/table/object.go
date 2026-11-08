package table

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// Redacted replaces Secret values wherever objects are shown or exported.
const Redacted = "<redacted by p9s>"

// ObjectYAML renders an object for display or export. managedFields are
// dropped, and Secret values are redacted: jumping to a violating Secret must
// never put credentials on screen, in a temp file, or in a report.
func ObjectYAML(obj *unstructured.Unstructured) string {
	clean := obj.DeepCopy()
	unstructured.RemoveNestedField(clean.Object, "metadata", "managedFields")
	if clean.GetKind() == "Secret" && clean.GroupVersionKind().Group == "" {
		for _, field := range []string{"data", "stringData"} {
			if m, ok, _ := unstructured.NestedMap(clean.Object, field); ok {
				for k := range m {
					m[k] = Redacted
				}
				_ = unstructured.SetNestedMap(clean.Object, m, field)
			}
		}
		unstructured.RemoveNestedField(clean.Object, "metadata", "annotations", "kubectl.kubernetes.io/last-applied-configuration")
	}
	b, err := yaml.Marshal(clean.Object)
	if err != nil {
		return "cannot render YAML: " + err.Error()
	}
	return strings.TrimRight(string(b), "\n")
}

// YAML renders any value as YAML (for detail sections).
func YAML(v any) string {
	b, err := yaml.Marshal(v)
	if err != nil {
		return "cannot render YAML: " + err.Error()
	}
	return strings.TrimRight(string(b), "\n")
}
