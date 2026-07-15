package dungeon

import (
	"strings"
	"testing"

	"github.com/PrPlanIT/PolySieve/internal/discovery"
	"github.com/PrPlanIT/PolySieve/internal/kube"
	"github.com/PrPlanIT/PolySieve/internal/profile"
)

// noCommitted is a CommittedReader with nothing on disk (the fully-derived path).
func noCommitted(string) ([]byte, error) { return nil, nil }

// fixture exercises: port resolution (foo:3000 → targetPort 8080), a numeric-passthrough
// (ceph-dashboard:8443), gateway exclusion (neko-gateway), a Gatus-annotated service, a
// statically probe-labelled workload, and an ingress-labelled workload (probe via the
// mutate-probe-on-ingress Kyverno policy, reproduced from the repo).
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
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: ingressapp
  namespace: hyrule-castle
spec:
  template:
    metadata:
      labels:
        policy.prplanit.com/ingress: "true"
    spec:
      containers:
        - ports:
            - containerPort: 6000
              protocol: TCP
`

func renderFixture(t *testing.T) map[string]string {
	t.Helper()
	m, _ := renderWith(t, fixture, noCommitted)
	return m
}

func renderWith(t *testing.T, src string, committed profile.CommittedReader) (map[string]string, profile.Report) {
	t.Helper()
	var objs kube.Objects
	if err := kube.Parse(&objs, []byte(src)); err != nil {
		t.Fatalf("parse: %v", err)
	}
	files, report, err := New().Render(discovery.Build(&objs), committed)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	m := make(map[string]string, len(files))
	for _, f := range files {
		m[f.Path] = string(f.Content)
	}
	return m, report
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
	// annotated svc 9000 + probe workload 7000 + ingress workload 6000 (probe-via-Kyverno).
	// Sorted: 6000, 7000, 9000.
	want := ciliumGatusSkeleton +
		"            - port: \"6000\"\n              protocol: TCP\n" +
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

// blindFixture: an ingress route to a Service with a selector that no repo workload satisfies —
// i.e. a Helm/operator-rendered backend PolySieve cannot see the pods of.
const blindFixture = `
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: gitlab
  namespace: hyrule-castle
spec:
  parentRefs:
    - name: xylem-gateway
  rules:
    - backendRefs:
        - name: gitlab-webservice
          port: 8181
---
apiVersion: v1
kind: Service
metadata:
  name: gitlab-webservice
  namespace: hyrule-castle
spec:
  selector:
    app: gitlab-webservice
  ports:
    - port: 8181
      targetPort: 8181
`

const gatusPath = ciliumBaseDir + "/ccnp-allow-gatus-healthcheck.yaml"

// committedGatus returns a CommittedReader serving the given rendered port entries as the
// on-disk gatus file.
func committedGatus(ports ...string) profile.CommittedReader {
	var b strings.Builder
	for _, p := range ports {
		b.WriteString("            - port: \"" + p + "\"\n              protocol: TCP\n")
	}
	body := b.String()
	return func(path string) ([]byte, error) {
		if strings.HasSuffix(path, "ccnp-allow-gatus-healthcheck.yaml") {
			return []byte(body), nil
		}
		return nil, nil
	}
}

// A blind ingress backend is reported, its routed port is derived, and any committed-only port
// (one PolySieve cannot see) is PRESERVED — never pruned.
func TestBlindPreservesCommittedPorts(t *testing.T) {
	m, report := renderWith(t, blindFixture, committedGatus("8181", "9168"))

	if len(report.BlindServices) != 1 || report.BlindServices[0] != "hyrule-castle/gitlab-webservice" {
		t.Fatalf("blind services = %v, want [hyrule-castle/gitlab-webservice]", report.BlindServices)
	}
	gatus := m[gatusPath]
	if !strings.Contains(gatus, `- port: "8181"`) {
		t.Errorf("routed port 8181 (derived) missing:\n%s", gatus)
	}
	if !strings.Contains(gatus, `- port: "9168"`) {
		t.Errorf("committed-only port 9168 was pruned despite blind backend — the honesty gate failed:\n%s", gatus)
	}
}

// With full coverage (no blind backends), a committed-only stale port must NOT survive — the
// derived set is authoritative and pruning is safe.
func TestFullCoveragePrunesStalePorts(t *testing.T) {
	m, report := renderWith(t, fixture, committedGatus("55555"))

	if len(report.BlindServices) != 0 {
		t.Fatalf("expected no blind services, got %v", report.BlindServices)
	}
	if strings.Contains(m[gatusPath], "55555") {
		t.Errorf("stale port 55555 preserved despite full coverage (should prune):\n%s", m[gatusPath])
	}
}

// ServiceHasVisibleWorkload: a selector matched by a repo workload is visible; an unmatched
// selector is blind; no selector is visible (external/manually-endpointed).
func TestServiceVisibility(t *testing.T) {
	src := `
apiVersion: v1
kind: Service
metadata:
  name: seen
  namespace: lost-woods
spec:
  selector: { app: seen }
  ports: [{ port: 80, targetPort: 80 }]
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: seen
  namespace: lost-woods
spec:
  template:
    metadata:
      labels: { app: seen }
    spec:
      containers: [{ ports: [{ containerPort: 80 }] }]
---
apiVersion: v1
kind: Service
metadata:
  name: unseen
  namespace: lost-woods
spec:
  selector: { app: unseen }
  ports: [{ port: 80, targetPort: 80 }]
---
apiVersion: v1
kind: Service
metadata:
  name: external
  namespace: lost-woods
spec:
  ports: [{ port: 80, targetPort: 80 }]
`
	var objs kube.Objects
	if err := kube.Parse(&objs, []byte(src)); err != nil {
		t.Fatalf("parse: %v", err)
	}
	g := discovery.Build(&objs)
	cases := map[string]bool{"seen": true, "unseen": false, "external": true}
	for svc, want := range cases {
		if got := g.ServiceHasVisibleWorkload("lost-woods", svc); got != want {
			t.Errorf("ServiceHasVisibleWorkload(%s) = %v, want %v", svc, got, want)
		}
	}
}

// routeBlindFixture: an ingress route to a Service that isn't in the repo at all, so its port
// can't be resolved — ResolvePort falls back to a guess.
const routeBlindFixture = `
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: grafana
  namespace: gossip-stone
spec:
  parentRefs:
    - name: phloem-gateway
  rules:
    - backendRefs:
        - name: grafana
          port: 80
`

// A route-blind backend (unresolved port) is reported; its guessed port is NOT asserted, and the
// committed route policy's real ports are preserved rather than pruned against the guess.
func TestRouteBlindPreservesCommittedPorts(t *testing.T) {
	committed := func(p string) ([]byte, error) {
		if strings.HasSuffix(p, "gossip-stone/allow-gateway-ingress-phloem.yaml") {
			return []byte("            ports: [\"3000\"]\n"), nil
		}
		return nil, nil
	}
	m, report := renderWith(t, routeBlindFixture, committed)

	if len(report.RouteBlindServices) != 1 || report.RouteBlindServices[0] != "gossip-stone/grafana" {
		t.Fatalf("route-blind services = %v, want [gossip-stone/grafana]", report.RouteBlindServices)
	}
	istio := m[istioOverlay+"/gossip-stone/allow-gateway-ingress-phloem.yaml"]
	if !strings.Contains(istio, `"3000"`) {
		t.Errorf("committed port 3000 pruned despite route-blind backend:\n%s", istio)
	}
	if strings.Contains(istio, `"80"`) {
		t.Errorf("guessed fallback port 80 was asserted — should be excluded:\n%s", istio)
	}
}
