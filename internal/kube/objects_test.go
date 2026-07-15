package kube

import "testing"

// Parse must unwrap a `kind: List` (as `kubectl get -o yaml` returns) into its typed items,
// so live-cluster augmentation feeds the same object set as a kustomize stream.
func TestParseList(t *testing.T) {
	const stream = `
apiVersion: v1
kind: List
items:
  - apiVersion: v1
    kind: Service
    metadata:
      name: grafana
      namespace: gossip-stone
    spec:
      selector: { app: grafana }
      ports:
        - port: 80
          targetPort: http
  - apiVersion: apps/v1
    kind: Deployment
    metadata:
      name: grafana
      namespace: gossip-stone
    spec:
      template:
        metadata:
          labels: { app: grafana, policy.prplanit.com/probe: "true" }
        spec:
          containers:
            - ports:
                - name: http
                  containerPort: 3000
  - apiVersion: v1
    kind: ConfigMap
    metadata: { name: ignored }
`
	var objs Objects
	if err := Parse(&objs, []byte(stream)); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(objs.Services) != 1 || objs.Services[0].Metadata.Name != "grafana" {
		t.Fatalf("services = %+v, want 1 (grafana)", objs.Services)
	}
	if len(objs.Workloads) != 1 {
		t.Fatalf("workloads = %d, want 1", len(objs.Workloads))
	}
	w := objs.Workloads[0]
	if w.Spec.Template.Metadata.Labels["policy.prplanit.com/probe"] != "true" {
		t.Errorf("probe label not parsed from List item: %+v", w.Spec.Template.Metadata.Labels)
	}
	if len(w.Spec.Template.Spec.Containers) != 1 ||
		w.Spec.Template.Spec.Containers[0].Ports[0].Name != "http" ||
		w.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort != 3000 {
		t.Errorf("named container port not parsed from List item: %+v", w.Spec.Template.Spec.Containers)
	}
}
