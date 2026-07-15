// Package dungeon is PolySieve profile #1 — the PrPlanIT "dungeon" GitOps cluster. It
// reproduces the output of the repo's two legacy generators (gen-cilium-backend-ports.sh
// and gen-gateway-ingress-policies.sh) byte-for-byte, from repo manifests instead of the
// live cluster.
package dungeon

import (
	"fmt"
	"sort"
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
	gatusEnabled  = "gatus.home-operations.com/enabled"
	gatusEndpoint = "gatus.home-operations.com/endpoint"
)

func (d dungeon) Render(g *discovery.RouteGraph) ([]profile.File, error) {
	var files []profile.File

	files = append(files, d.ciliumContract(g), d.ciliumGatus(g))
	files = append(files, d.istioGatewayIngress(g)...)
	return files, nil
}

// ── Cilium: contract-ingress-backend ─────────────────────────────────────────
// Every resolved backend port reachable through the three ingress gateways, cluster-wide.

func (d dungeon) ciliumContract(g *discovery.RouteGraph) profile.File {
	var ports []int
	for _, b := range g.Backends {
		if classify(b.Gateway) == "" {
			continue
		}
		ports = append(ports, b.Port)
	}
	ports = discovery.SortDedupInts(ports)
	return profile.File{
		Path:    ciliumBaseDir + "/ccnp-contract-ingress-backend.yaml",
		Content: []byte(ciliumContractSkeleton + ciliumPortBlock(ports)),
	}
}

// ── Cilium: allow-gatus-healthcheck ──────────────────────────────────────────
// Ports Gatus probes: annotated-service targetPorts + probe-labelled workload containerPorts.

func (d dungeon) ciliumGatus(g *discovery.RouteGraph) profile.File {
	var ports []int
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
	ports = append(ports, g.WorkloadsWithPodLabel(probeLabel, "true")...)
	ports = discovery.SortDedupInts(ports)
	return profile.File{
		Path:    ciliumBaseDir + "/ccnp-allow-gatus-healthcheck.yaml",
		Content: []byte(ciliumGatusSkeleton + ciliumPortBlock(ports)),
	}
}

// ── Istio: per-namespace gateway-ingress AuthorizationPolicies ───────────────
// One file per (backend namespace, gateway class), allowing that gateway's SA to reach the
// namespace's ingress-labelled pods on the resolved backend ports.

func (d dungeon) istioGatewayIngress(g *discovery.RouteGraph) []profile.File {
	type key struct{ ns, class string }
	groups := map[key][]int{}
	for _, b := range g.Backends {
		c := classify(b.Gateway)
		if c == "" {
			continue
		}
		k := key{b.BackendNS, c}
		groups[k] = append(groups[k], b.Port)
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
		sa := gatewaySA[k.class]
		files = append(files, profile.File{
			Path: fmt.Sprintf("%s/%s/allow-gateway-ingress-%s.yaml", istioOverlay, k.ns, k.class),
			Content: []byte(fmt.Sprintf(istioTemplate,
				k.class,                                  // metadata.name suffix
				sa[0], sa[1],                             // principal ns / name
				istioPortList(discovery.SortDedupInts(groups[k])), // inline flow list
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
