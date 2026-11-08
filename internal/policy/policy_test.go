package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PrPlanIT/PolySieve/internal/config"
	"github.com/PrPlanIT/PolySieve/internal/discovery"
	"github.com/PrPlanIT/PolySieve/internal/kube"
)

// noCommitted is a CommittedReader with nothing on disk (the fully-derived path).
func noCommitted(string) ([]byte, error) { return nil, nil }

const (
	contractPath = "out/cilium/contract.yaml"
	gatusPath    = "out/cilium/gatus.yaml"
	istioDir     = "out/istio"
)

// testConfig selects ingress gateways by label, as a repo with its own label model does.
func testConfig() config.Config {
	cfg := config.Default()
	cfg.Overlay(config.Config{
		Labels: config.Labels{Ingress: "example.com/ingress", Probe: "example.com/probe"},
		Gateways: config.Gateways{
			Selector:      map[string]string{"example.com/client-class": "ingress-gateway"},
			InstanceLabel: "example.com/ingress-instance",
			PodSelector:   map[string]string{"example.com/client-class": "ingress-gateway"},
		},
	})
	cfg.Outputs.Contract.Path = contractPath
	cfg.Outputs.Healthcheck.Path = gatusPath
	cfg.Outputs.GatewayIngress.Dir = istioDir
	return cfg
}

// gateways are the Gateway objects every fixture is rendered against. media-gateway carries no
// contract label, so it must be excluded by the selector, not by name.
const gateways = `
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: public-gateway
  namespace: gw-public
spec:
  gatewayClassName: istio
  infrastructure:
    labels:
      example.com/client-class: ingress-gateway
      example.com/ingress-instance: public
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: internal-gateway
  namespace: gw-internal
spec:
  gatewayClassName: istio
  infrastructure:
    labels:
      example.com/client-class: ingress-gateway
      example.com/ingress-instance: internal
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: media-gateway
  namespace: apps
spec:
  gatewayClassName: stunner
  infrastructure:
    labels:
      app: media
`

// prober is the health-prober workload; its namespace is discovered, not configured.
const prober = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: gatus
  namespace: monitoring
spec:
  template:
    metadata:
      labels:
        app.kubernetes.io/name: gatus
`

// fixture exercises: port resolution (web:3000 → targetPort 8080), a numeric passthrough
// (dashboard:8443), gateway exclusion (media-gateway), an annotated service, a statically
// probe-labelled workload, and an ingress-labelled workload.
const fixture = `
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: dashboard
  namespace: storage
spec:
  parentRefs:
    - name: public-gateway
  rules:
    - backendRefs:
        - name: dashboard
          port: 8443
---
apiVersion: v1
kind: Service
metadata:
  name: dashboard
  namespace: storage
spec:
  ports:
    - port: 8443
      targetPort: 8443
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: web
  namespace: apps
spec:
  parentRefs:
    - name: internal-gateway
  rules:
    - backendRefs:
        - name: web
          port: 3000
---
apiVersion: v1
kind: Service
metadata:
  name: web
  namespace: apps
spec:
  ports:
    - port: 3000
      targetPort: 8080
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: excluded
  namespace: apps
spec:
  parentRefs:
    - name: media-gateway
  rules:
    - backendRefs:
        - name: media
          port: 9999
---
apiVersion: v1
kind: Service
metadata:
  name: probed
  namespace: tools
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
  namespace: tools
spec:
  template:
    metadata:
      labels:
        example.com/probe: "true"
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
  namespace: apps
spec:
  template:
    metadata:
      labels:
        example.com/ingress: "true"
    spec:
      containers:
        - ports:
            - containerPort: 6000
              protocol: TCP
`

func renderFixture(t *testing.T) map[string]string {
	t.Helper()
	m, _ := renderWith(t, testConfig(), fixture, noCommitted)
	return m
}

func renderWith(t *testing.T, cfg config.Config, src string, committed CommittedReader) (map[string]string, Report) {
	t.Helper()
	var objs kube.Objects
	for _, s := range []string{gateways, prober, src} {
		if err := kube.Parse(&objs, []byte(s)); err != nil {
			t.Fatalf("parse: %v", err)
		}
	}
	files, report, err := Render(cfg, discovery.Build(&objs), committed)
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
	// fromEndpoints: one block per discovered gateway namespace, sorted. media has no contract
	// label → excluded.
	wantSources := "        # internal-gateway\n" +
		"        - matchLabels:\n" +
		"            k8s:io.kubernetes.pod.namespace: gw-internal\n" +
		"            example.com/client-class: ingress-gateway\n" +
		"        # public-gateway\n" +
		"        - matchLabels:\n" +
		"            k8s:io.kubernetes.pod.namespace: gw-public\n" +
		"            example.com/client-class: ingress-gateway\n"
	// dashboard 8443 (numeric passthrough) + web 3000→8080; excluded media dropped.
	want := fmt.Sprintf(ciliumContractHeader, "contract-ingress-backend", "example.com/ingress", "example.com/ingress") +
		wantSources + ciliumToPorts +
		"            - port: \"8080\"\n              protocol: TCP\n" +
		"            - port: \"8443\"\n              protocol: TCP\n"
	if got := m[contractPath]; got != want {
		t.Errorf("contract bytes mismatch:\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}

func TestCiliumHealthcheck(t *testing.T) {
	m := renderFixture(t)
	// annotated svc 9000 + probe workload 7000 + ingress workload 6000. Sorted: 6000, 7000, 9000.
	want := fmt.Sprintf(ciliumHealthcheckHeader, "allow-gatus-healthcheck", "example.com/probe") +
		"        - matchLabels:\n" +
		"            k8s:io.kubernetes.pod.namespace: monitoring\n" +
		"            app.kubernetes.io/name: gatus\n" +
		ciliumToPorts +
		"            - port: \"6000\"\n              protocol: TCP\n" +
		"            - port: \"7000\"\n              protocol: TCP\n" +
		"            - port: \"9000\"\n              protocol: TCP\n"
	if got := m[gatusPath]; got != want {
		t.Errorf("healthcheck bytes mismatch:\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}

// With no prober workload in the object set the healthcheck can't name its source: an error,
// never a guess.
func TestHealthcheckWithoutProberFails(t *testing.T) {
	var objs kube.Objects
	if err := kube.Parse(&objs, []byte(gateways+"---\n"+fixture)); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, _, err := Render(testConfig(), discovery.Build(&objs), noCommitted); err == nil {
		t.Error("expected an error with no workload matching the prober selector")
	}
}

func TestIstioFiles(t *testing.T) {
	m := renderFixture(t)

	public := m[istioDir+"/storage/allow-gateway-ingress-public.yaml"]
	if !strings.Contains(public, "- cluster.local/ns/gw-public/sa/public-gateway-istio") {
		t.Errorf("public principal wrong:\n%s", public)
	}
	if !strings.Contains(public, "  name: allow-gateway-ingress-public\n") ||
		!strings.Contains(public, "      example.com/ingress: \"true\"\n") {
		t.Errorf("public name/selector wrong:\n%s", public)
	}
	if !strings.HasSuffix(public, "            ports: [\"8443\"]\n") {
		t.Errorf("public port list wrong:\n%s", public)
	}

	internal := m[istioDir+"/apps/allow-gateway-ingress-internal.yaml"]
	if !strings.Contains(internal, "- cluster.local/ns/gw-internal/sa/internal-gateway-istio") ||
		!strings.HasSuffix(internal, "            ports: [\"8080\"]\n") {
		t.Errorf("apps internal wrong (derived principal ns + resolved port 8080):\n%s", internal)
	}

	if _, ok := m[istioDir+"/apps/allow-gateway-ingress-media.yaml"]; ok {
		t.Error("media-gateway must be excluded — no file expected")
	}
}

// With no gateway selector, Istio-managed Gateways are the ingress gateways and the contract
// selects each one's pods by gateway name.
func TestIstioDefaults(t *testing.T) {
	cfg := config.Default()
	cfg.Outputs.Contract.Path = contractPath
	cfg.Outputs.Healthcheck.Path = gatusPath
	cfg.Outputs.GatewayIngress.Dir = istioDir
	m, _ := renderWith(t, cfg, fixture, noCommitted)

	contract := m[contractPath]
	for _, want := range []string{
		"            k8s:io.kubernetes.pod.namespace: gw-internal\n            gateway.networking.k8s.io/gateway-name: internal-gateway\n",
		"            k8s:io.kubernetes.pod.namespace: gw-public\n            gateway.networking.k8s.io/gateway-name: public-gateway\n",
		"      " + config.LabelPrefix + "ingress: \"true\"\n",
	} {
		if !strings.Contains(contract, want) {
			t.Errorf("contract missing %q:\n%s", want, contract)
		}
	}
	if strings.Contains(contract, "media-gateway") {
		t.Errorf("non-Istio media-gateway selected:\n%s", contract)
	}
	// No instance label on the Gateway → the name with "-gateway" removed.
	if _, ok := m[istioDir+"/storage/allow-gateway-ingress-public.yaml"]; !ok {
		t.Errorf("expected the public policy; got %v", keys(m))
	}
}

func TestDisabledOutputs(t *testing.T) {
	cfg := testConfig()
	off := false
	cfg.Outputs.Healthcheck.Enabled = &off
	cfg.Outputs.GatewayIngress.Enabled = &off
	m, _ := renderWith(t, cfg, fixture, noCommitted)
	if len(m) != 1 || m[contractPath] == "" {
		t.Errorf("expected only the contract; got %v", keys(m))
	}
}

func TestLocate(t *testing.T) {
	repo := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("net/cilium/c.yaml", derivedMarker+" x\nkind: CiliumClusterwideNetworkPolicy\nmetadata:\n  name: contract-ingress-backend\n")
	write("net/istio/apps/a.yaml", derivedMarker+" x\nkind: AuthorizationPolicy\nmetadata:\n  name: allow-gateway-ingress-public\n")
	write("other/hand.yaml", "kind: CiliumClusterwideNetworkPolicy\nmetadata:\n  name: allow-gatus-healthcheck\n")

	cfg := config.Default()
	if err := Locate(repo, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Outputs.Contract.Path != "net/cilium/c.yaml" {
		t.Errorf("contract path = %q", cfg.Outputs.Contract.Path)
	}
	if cfg.Outputs.Healthcheck.Path != defaultCiliumDir+"/ccnp-allow-gatus-healthcheck.yaml" {
		t.Errorf("healthcheck path = %q (an unmarked file must not count)", cfg.Outputs.Healthcheck.Path)
	}
	if cfg.Outputs.GatewayIngress.Dir != "net/istio" {
		t.Errorf("gateway-ingress dir = %q", cfg.Outputs.GatewayIngress.Dir)
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
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
	if got := istioPortList([]int{8080, 80, 3000}); got != "\"8080\", \"80\", \"3000\"" {
		t.Errorf("flow list format wrong: %s", got)
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
  name: git
  namespace: devtools
spec:
  parentRefs:
    - name: public-gateway
  rules:
    - backendRefs:
        - name: git-web
          port: 8181
---
apiVersion: v1
kind: Service
metadata:
  name: git-web
  namespace: devtools
spec:
  selector:
    app: git-web
  ports:
    - port: 8181
      targetPort: 8181
`

// committedGatus returns a CommittedReader serving the given rendered port entries as the
// on-disk healthcheck file.
func committedGatus(ports ...string) CommittedReader {
	var b strings.Builder
	for _, p := range ports {
		b.WriteString("            - port: \"" + p + "\"\n              protocol: TCP\n")
	}
	body := b.String()
	return func(path string) ([]byte, error) {
		if path == gatusPath {
			return []byte(body), nil
		}
		return nil, nil
	}
}

// A blind ingress backend is reported, its routed port is derived, and any committed-only port
// (one PolySieve cannot see) is PRESERVED — never pruned.
func TestBlindPreservesCommittedPorts(t *testing.T) {
	m, report := renderWith(t, testConfig(), blindFixture, committedGatus("8181", "9168"))

	if len(report.BlindServices) != 1 || report.BlindServices[0] != "devtools/git-web" {
		t.Fatalf("blind services = %v, want [devtools/git-web]", report.BlindServices)
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
	m, report := renderWith(t, testConfig(), fixture, committedGatus("55555"))

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
  namespace: apps
spec:
  selector: { app: seen }
  ports: [{ port: 80, targetPort: 80 }]
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: seen
  namespace: apps
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
  namespace: apps
spec:
  selector: { app: unseen }
  ports: [{ port: 80, targetPort: 80 }]
---
apiVersion: v1
kind: Service
metadata:
  name: external
  namespace: apps
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
		if got := g.ServiceHasVisibleWorkload("apps", svc); got != want {
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
  namespace: monitoring
spec:
  parentRefs:
    - name: internal-gateway
  rules:
    - backendRefs:
        - name: grafana
          port: 80
`

// A route-blind backend (unresolved port) is reported; its guessed port is NOT asserted, and the
// committed route policy's real ports are preserved rather than pruned against the guess.
func TestRouteBlindPreservesCommittedPorts(t *testing.T) {
	istioPath := istioDir + "/monitoring/allow-gateway-ingress-internal.yaml"
	committed := func(p string) ([]byte, error) {
		if p == istioPath {
			return []byte("            ports: [\"3000\"]\n"), nil
		}
		return nil, nil
	}
	m, report := renderWith(t, testConfig(), routeBlindFixture, committed)

	if len(report.RouteBlindServices) != 1 || report.RouteBlindServices[0] != "monitoring/grafana" {
		t.Fatalf("route-blind services = %v, want [monitoring/grafana]", report.RouteBlindServices)
	}
	istio := m[istioPath]
	if !strings.Contains(istio, `"3000"`) {
		t.Errorf("committed port 3000 pruned despite route-blind backend:\n%s", istio)
	}
	if strings.Contains(istio, `"80"`) {
		t.Errorf("guessed fallback port 80 was asserted — should be excluded:\n%s", istio)
	}
}
