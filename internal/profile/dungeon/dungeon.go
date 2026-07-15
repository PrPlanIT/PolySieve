// Package dungeon is PolySieve profile #1 — the PrPlanIT "dungeon" GitOps cluster. It
// reproduces the output of the repo's two legacy generators (gen-cilium-backend-ports.sh
// and gen-gateway-ingress-policies.sh) byte-for-byte, from repo manifests instead of the
// live cluster.
package dungeon

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/PrPlanIT/PolySieve/internal/discovery"
	"github.com/PrPlanIT/PolySieve/internal/profile"
)

// New returns the dungeon profile.
func New() profile.Profile { return dungeon{} }

type dungeon struct{}

func (dungeon) Name() string { return "dungeon" }

// Roots are the Flux-reconciled kustomize build directories that carry the dungeon's
// HTTPRoutes, Services, EndpointSlices and workloads — the repo-driven equivalent of the
// reference generators' cluster-wide `kubectl get`. Overridable at the CLI with --root.
func (dungeon) Roots() []string {
	return []string{
		"fluxcd/apps/overlays/production",
		"fluxcd/infrastructure/configs/overlays/production",
		"fluxcd/infrastructure/services/overlays/production/phase-01-storage",
		"fluxcd/infrastructure/services/overlays/production/phase-02-critical",
		"fluxcd/infrastructure/services/overlays/production/phase-03-core",
		"fluxcd/infrastructure/services/overlays/production/phase-04-platform",
	}
}

// ── Gateway model ────────────────────────────────────────────────────────────
// A route's parentRef name maps to a short gateway class by prefix; unrecognised gateways
// (e.g. neko-gateway) are excluded. Each class has an Istio ServiceAccount identity.

var gatewayClasses = []struct{ prefix, class string }{
	{"xylem-gateway", "xylem"},
	{"phloem-gateway", "phloem"},
	{"cell-membrane-gateway", "cell-membrane"},
}

var gatewaySA = map[string][2]string{ // class → {SA namespace, SA name}
	"xylem":         {"arylls-lookout", "xylem-gateway-istio"},
	"phloem":        {"kokiri-forest", "phloem-gateway-istio"},
	"cell-membrane": {"hyrule-castle", "cell-membrane-gateway-istio"},
}

func classify(gateway string) string {
	for _, gc := range gatewayClasses {
		if strings.HasPrefix(gateway, gc.prefix) {
			return gc.class
		}
	}
	return ""
}

const (
	ciliumBaseDir = "fluxcd/infrastructure/configs/base/cilium-policies"
	istioOverlay  = "fluxcd/infrastructure/configs/overlays/production/istio-policies"
	probeLabel    = "policy.prplanit.com/probe"
	ingressLabel  = "policy.prplanit.com/ingress"
	gatusEnabled  = "gatus.home-operations.com/enabled"
	gatusEndpoint = "gatus.home-operations.com/endpoint"
)

func (d dungeon) Render(g *discovery.RouteGraph, committed profile.CommittedReader) ([]profile.File, profile.Report, error) {
	blind := blindIngressServices(g)
	routeBlind := routeBlindServices(g)

	var files []profile.File
	files = append(files, d.ciliumContract(g, committed), d.ciliumGatus(g, blind, committed))
	files = append(files, d.istioGatewayIngress(g, committed)...)
	return files, profile.Report{BlindServices: blind, RouteBlindServices: routeBlind}, nil
}

// blindIngressServices lists the ingress backends ("ns/service", sorted, unique) whose serving
// workload is not visible in the repo — rendered by Helm or an operator. These are the backends
// whose non-routed (container) ports the repo cannot describe, so the Gatus healthcheck
// preserves rather than prunes while any remain unresolved.
func blindIngressServices(g *discovery.RouteGraph) []string {
	return uniqueBackendServices(g, func(b discovery.Backend) bool {
		return !g.ServiceHasVisibleWorkload(b.BackendNS, b.Service)
	})
}

// routeBlindServices lists the ingress backends whose routed port could not be resolved from the
// repo (Service absent, or a named targetPort with no EndpointSlice) — the port is a fallback
// guess. The route-derived policies covering these backends preserve rather than prune.
func routeBlindServices(g *discovery.RouteGraph) []string {
	return uniqueBackendServices(g, func(b discovery.Backend) bool { return !b.Resolved })
}

// uniqueBackendServices returns the sorted, unique "ns/service" of classified ingress backends
// matching pred.
func uniqueBackendServices(g *discovery.RouteGraph, pred func(discovery.Backend) bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range g.Backends {
		if classify(b.Gateway) == "" || !pred(b) {
			continue
		}
		key := b.BackendNS + "/" + b.Service
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// ── Cilium: contract-ingress-backend ─────────────────────────────────────────
// Every resolved backend port reachable through the three ingress gateways, cluster-wide.

func (d dungeon) ciliumContract(g *discovery.RouteGraph, committed profile.CommittedReader) profile.File {
	path := ciliumBaseDir + "/ccnp-contract-ingress-backend.yaml"
	var ports []int
	blind := false
	for _, b := range g.Backends {
		if classify(b.Gateway) == "" {
			continue
		}
		if !b.Resolved {
			blind = true // route-blind: its real port is unknown, don't assert the guess
			continue
		}
		ports = append(ports, b.Port)
	}
	// Honesty gate: a route-blind backend's real port is unknown, so preserve the committed
	// contract rather than prune against a guess.
	if blind {
		if prev, err := committed(path); err == nil {
			ports = append(ports, extractPorts(prev)...)
		}
	}
	ports = discovery.SortDedupInts(ports)
	return profile.File{
		Path:    path,
		Content: []byte(ciliumContractSkeleton + ciliumPortBlock(ports)),
	}
}

// ── Cilium: allow-gatus-healthcheck ──────────────────────────────────────────
// Ports Gatus probes: annotated-service targetPorts + probe-labelled workload containerPorts.

func (d dungeon) ciliumGatus(g *discovery.RouteGraph, blind []string, committed profile.CommittedReader) profile.File {
	path := ciliumBaseDir + "/ccnp-allow-gatus-healthcheck.yaml"

	var ports []int
	// (a) Ports of Gatus-annotated services, resolved to their pod targetPorts.
	for _, s := range g.Objects.Services {
		ann := s.Metadata.Annotations
		_, hasEndpoint := ann[gatusEndpoint]
		if ann[gatusEnabled] != "true" && !hasEndpoint {
			continue
		}
		for _, sp := range s.Spec.Ports {
			ports = append(ports, g.ResolvePort(s.Metadata.Namespace, s.Metadata.Name, sp.Port))
		}
	}
	// (b) Container ports of probe-labelled workloads. The label is rarely set by hand: the
	// cluster's Kyverno ClusterPolicy `mutate-probe-on-ingress` stamps probe=true onto any
	// Deployment/StatefulSet/DaemonSet whose pod template carries ingress=true, at admission.
	// Reading the repo we never see that mutation, so reproduce it by unioning the
	// ingress-labelled workloads with the few that carry probe=true statically.
	ports = append(ports, g.WorkloadsWithPodLabel(probeLabel, "true")...)
	ports = append(ports, g.WorkloadsWithPodLabel(ingressLabel, "true")...)
	// (c) For ingress backends whose workload is invisible (Helm/operator), we can't read pod
	// labels or container ports; contribute at least the resolved routed port, since a route
	// backend behind an ingress gateway is probe-eligible. Non-routed ports are covered by the
	// preserve gate below.
	for _, b := range g.Backends {
		if classify(b.Gateway) == "" || g.ServiceHasVisibleWorkload(b.BackendNS, b.Service) {
			continue
		}
		if !b.Resolved {
			continue // don't contribute a guessed port; the preserve gate keeps the committed one
		}
		ports = append(ports, b.Port)
	}
	// Honesty gate: while any ingress backend is blind we cannot know its non-routed ports, so
	// we must not prune anything the committed policy already allows. Fold the committed ports
	// in — additive only — and let the run report the blind set. When nothing is blind the
	// derived set is authoritative and pruning is safe.
	if len(blind) > 0 {
		if prev, err := committed(path); err == nil {
			ports = append(ports, extractPorts(prev)...)
		}
	}
	ports = discovery.SortDedupInts(ports)
	return profile.File{Path: path, Content: []byte(ciliumGatusSkeleton + ciliumPortBlock(ports))}
}

// Port extractors for the two rendered policy formats: the cilium block (`- port: "8200"`)
// and the istio inline list (`ports: ["80", "3000"]`). They don't overlap — a cilium file has
// no `ports: [` and an istio file has no `- port:` — so scanning for both is safe on either.
var (
	ciliumPortRe = regexp.MustCompile(`- port:\s*"?(\d+)"?`)
	istioPortsRe = regexp.MustCompile(`ports:\s*\[([^\]]*)\]`)
	numRe        = regexp.MustCompile(`\d+`)
)

// extractPorts pulls the numeric ports out of an already-rendered policy file, used by the
// honesty gate to preserve a committed policy's existing ports under partial coverage.
func extractPorts(b []byte) []int {
	var out []int
	for _, m := range ciliumPortRe.FindAllSubmatch(b, -1) {
		if n, err := strconv.Atoi(string(m[1])); err == nil {
			out = append(out, n)
		}
	}
	for _, m := range istioPortsRe.FindAllSubmatch(b, -1) {
		for _, num := range numRe.FindAll(m[1], -1) {
			if n, err := strconv.Atoi(string(num)); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

// ── Istio: per-namespace gateway-ingress AuthorizationPolicies ───────────────
// One file per (backend namespace, gateway class), allowing that gateway's SA to reach the
// namespace's ingress-labelled pods on the resolved backend ports.

func (d dungeon) istioGatewayIngress(g *discovery.RouteGraph, committed profile.CommittedReader) []profile.File {
	type key struct{ ns, class string }
	type group struct {
		ports []int
		blind bool // some backend in this group has an unresolved (guessed) port
	}
	groups := map[key]*group{}
	for _, b := range g.Backends {
		c := classify(b.Gateway)
		if c == "" {
			continue
		}
		k := key{b.BackendNS, c}
		gr := groups[k]
		if gr == nil {
			gr = &group{}
			groups[k] = gr
		}
		if !b.Resolved {
			gr.blind = true
			continue
		}
		gr.ports = append(gr.ports, b.Port)
	}

	// Deterministic file order: sort keys by "ns|class" (matches the reference's key sort).
	keys := make([]key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].ns+"|"+keys[i].class < keys[j].ns+"|"+keys[j].class
	})

	var files []profile.File
	for _, k := range keys {
		gr := groups[k]
		sa := gatewaySA[k.class]
		path := fmt.Sprintf("%s/%s/allow-gateway-ingress-%s.yaml", istioOverlay, k.ns, k.class)
		ports := gr.ports
		// Honesty gate: if any backend for this namespace routed to a guessed port, preserve the
		// committed policy's ports rather than prune against the guess.
		if gr.blind {
			if prev, err := committed(path); err == nil {
				ports = append(ports, extractPorts(prev)...)
			}
		}
		files = append(files, profile.File{
			Path: path,
			Content: []byte(fmt.Sprintf(istioTemplate,
				k.class,      // metadata.name suffix
				sa[0], sa[1], // principal ns / name
				istioPortList(discovery.SortDedupInts(ports)), // inline flow list
			)),
		})
	}
	return files
}

// ── Byte-exact rendering helpers ─────────────────────────────────────────────

// ciliumPortBlock renders the port sequence appended after a `- ports:` line. Cilium caps a
// toPorts entry at 40 ports; overflow chunks emit their own `- ports:` wrapper at 8 spaces.
// Port items are at 12 spaces; protocol at 14. Ports are already sort-deduped by the caller.
func ciliumPortBlock(ports []int) string {
	const (
		indent       = "            " // 12 spaces
		prefixIndent = "        "     // 8 spaces
		maxPerBlock  = 40
	)
	var b strings.Builder
	for start := 0; start < len(ports); start += maxPerBlock {
		if start > 0 {
			b.WriteString(prefixIndent + "- ports:\n")
		}
		end := start + maxPerBlock
		if end > len(ports) {
			end = len(ports)
		}
		for _, p := range ports[start:end] {
			fmt.Fprintf(&b, "%s- port: \"%d\"\n", indent, p)
			b.WriteString(indent + "  protocol: TCP\n")
		}
	}
	return b.String()
}

// istioPortList renders the inline flow sequence, e.g. `"80", "3000", "8080"`.
func istioPortList(ports []int) string {
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = fmt.Sprintf("%q", fmt.Sprint(p))
	}
	return strings.Join(parts, ", ")
}
