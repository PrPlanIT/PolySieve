package dungeon

import (
	"strings"
	"testing"

	"github.com/PrPlanIT/PolySieve/internal/discovery"
	"github.com/PrPlanIT/PolySieve/internal/kube"
)

// fixture exercises: port resolution (foo:3000 → targetPort 8080), a numeric-passthrough
// (ceph-dashboard:8443), gateway exclusion (neko-gateway), a Gatus-annotated service, and a
// probe-labelled workload.
const fixture = `
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: ceph-dashboard
  namespace: gorons-bracelet
spec:
  parentRefs:
    - name: xylem-gateway
  rules:
    - backendRefs:
        - name: ceph-dashboard
          port: 8443
---
apiVersion: v1
kind: Service
metadata:
  name: ceph-dashboard
  namespace: gorons-bracelet
spec:
  ports:
    - port: 8443
      targetPort: 8443
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: foo
  namespace: tingle-tuner
spec:
  parentRefs:
    - name: phloem-gateway
  rules:
    - backendRefs:
        - name: foo
          port: 3000
---
apiVersion: v1
kind: Service
metadata:
  name: foo
  namespace: tingle-tuner
spec:
  ports:
    - port: 3000
      targetPort: 8080
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: excluded
  namespace: tingle-tuner
spec:
  parentRefs:
    - name: neko-gateway
  rules:
    - backendRefs:
        - name: nekobackend
          port: 9999
---
apiVersion: v1
kind: Service
metadata:
  name: gatussvc
  namespace: temple-of-time
  annotations:
    gatus.home-operations.com/enabled: "true"
spec:
  ports:
    - port: 9000
      targetPort: 9000
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: probeapp
  namespace: lost-woods
spec:
  template:
    metadata:
      labels:
        policy.prplanit.com/probe: "true"
    spec:
      containers:
        - ports:
            - containerPort: 7000
              protocol: TCP
`

func renderFixture(t *testing.T) map[string]string {
	t.Helper()
	var objs kube.Objects
	if err := kube.Parse(&objs, []byte(fixture)); err != nil {
		t.Fatalf("parse: %v", err)
	}
	files, err := New().Render(discovery.Build(&objs))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	m := make(map[string]string, len(files))
	for _, f := range files {
		m[f.Path] = string(f.Content)
	}
	return m
}

func TestCiliumContract(t *testing.T) {
	m := renderFixture(t)
	// ceph 8443 (numeric passthrough) + foo 3000→8080; excluded neko dropped. Sorted: 8080, 8443.
	want := ciliumContractSkeleton +
		"            - port: \"8080\"\n              protocol: TCP\n" +
		"            - port: \"8443\"\n              protocol: TCP\n"
	got := m[ciliumBaseDir+"/ccnp-contract-ingress-backend.yaml"]
	if got != want {
		t.Errorf("contract bytes mismatch:\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}

func TestCiliumGatus(t *testing.T) {
	m := renderFixture(t)
	// annotated svc 9000 + probe workload 7000. Sorted: 7000, 9000.
	want := ciliumGatusSkeleton +
		"            - port: \"7000\"\n              protocol: TCP\n" +
		"            - port: \"9000\"\n              protocol: TCP\n"
	got := m[ciliumBaseDir+"/ccnp-allow-gatus-healthcheck.yaml"]
	if got != want {
		t.Errorf("gatus bytes mismatch:\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}

func TestIstioFiles(t *testing.T) {
	m := renderFixture(t)

	xylem := m[istioOverlay+"/gorons-bracelet/allow-gateway-ingress-xylem.yaml"]
	if !strings.Contains(xylem, "- cluster.local/ns/arylls-lookout/sa/xylem-gateway-istio") {
		t.Errorf("xylem principal wrong:\n%s", xylem)
	}
	if !strings.HasSuffix(xylem, "            ports: [\"8443\"]\n") {
		t.Errorf("xylem port list wrong:\n%s", xylem)
	}
	if !strings.HasPrefix(xylem, "# DERIVED: pod targetPorts") {
		t.Errorf("xylem header wrong:\n%s", xylem)
	}

	phloem := m[istioOverlay+"/tingle-tuner/allow-gateway-ingress-phloem.yaml"]
	if !strings.Contains(phloem, "sa/phloem-gateway-istio") ||
		!strings.HasSuffix(phloem, "            ports: [\"8080\"]\n") {
		t.Errorf("tingle-tuner phloem wrong (resolved port 8080):\n%s", phloem)
	}

	if _, ok := m[istioOverlay+"/tingle-tuner/allow-gateway-ingress-neko.yaml"]; ok {
		t.Error("neko-gateway must be excluded — no file expected")
	}
}

func TestCiliumChunking(t *testing.T) {
	ports := make([]int, 41)
	for i := range ports {
		ports[i] = 1000 + i
	}
	block := ciliumPortBlock(ports)
	// 41 ports → first 40 under the template's `- ports:`, then one extra wrapper for #41.
	if n := strings.Count(block, "        - ports:\n"); n != 1 {
		t.Errorf("expected exactly 1 extra `- ports:` wrapper for 41 ports, got %d", n)
	}
	if n := strings.Count(block, "- port:"); n != 41 {
		t.Errorf("expected 41 port entries, got %d", n)
	}
}

func TestMultiPortSortAndFlow(t *testing.T) {
	// numeric (not lexical) sort inside an inline list: 80 < 3000 < 8080.
	if got := istioPortList([]int{8080, 80, 3000}); got != "\"80\", \"3000\", \"8080\"" {
		// istioPortList sorts? No — caller sorts. Verify format only here.
		if got != "\"8080\", \"80\", \"3000\"" {
			t.Errorf("flow list format wrong: %s", got)
		}
	}
	if got := istioPortList(discovery.SortDedupInts([]int{8080, 80, 3000})); got != "\"80\", \"3000\", \"8080\"" {
		t.Errorf("sorted flow list wrong: %s", got)
	}
}
