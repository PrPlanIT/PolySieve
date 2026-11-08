// Package policy renders a RouteGraph into policy files: a Cilium ingress-backend contract, a
// Cilium health-prober allowance, and per-namespace Istio gateway-ingress AuthorizationPolicies.
// Label keys, gateway selection, names and paths come from the config.
package policy

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/PrPlanIT/PolySieve/internal/config"
	"github.com/PrPlanIT/PolySieve/internal/discovery"
)

// File is one policy file to emit: a repo-relative path and its exact byte content.
type File struct {
	Path    string
	Content []byte
}

// CommittedReader returns the current committed bytes of a repo-relative path (nil, nil when
// absent). The honesty gate uses it to preserve ports it cannot re-derive under partial
// coverage, so a port that can't be accounted for is never silently pruned.
type CommittedReader func(path string) ([]byte, error)

// Report carries the coverage facts a run must surface so PolySieve never prunes silently.
type Report struct {
	// BlindServices are ingress backends ("ns/service") whose serving workload is not visible
	// in the repo (Helm- or operator-rendered). Their non-routed ports cannot be derived, so a
	// policy that depends on them (the health-prober allowance) was emitted in preserve mode.
	BlindServices []string
	// RouteBlindServices are ingress backends whose routed port could not be resolved from the
	// repo — the Service is absent or its targetPort is a named port with no EndpointSlice — so
	// the port is a fallback guess. The route-derived policies for their files were emitted in
	// preserve mode rather than pruned against a guess.
	RouteBlindServices []string
}

// gatewayNameLabel is the label Istio stamps on a Gateway's data-plane pods.
const gatewayNameLabel = "gateway.networking.k8s.io/gateway-name"

// Render derives the enabled policy files. Output paths must already be resolved (see Locate).
func Render(cfg config.Config, g *discovery.RouteGraph, committed CommittedReader) ([]File, Report, error) {
	gws := g.IngressGateways(discovery.GatewaySelection{
		Selector:      cfg.Gateways.Selector,
		InstanceLabel: cfg.Gateways.InstanceLabel,
	})
	blind := blindIngressServices(g, gws)
	routeBlind := routeBlindServices(g, gws)

	var files []File
	out := cfg.Outputs
	if config.On(out.Contract.Enabled) {
		files = append(files, ciliumContract(cfg, g, gws, committed))
	}
	if config.On(out.Healthcheck.Enabled) {
		f, err := ciliumHealthcheck(cfg, g, gws, blind, committed)
		if err != nil {
			return nil, Report{}, err
		}
		files = append(files, f)
	}
	if config.On(out.GatewayIngress.Enabled) {
		files = append(files, istioGatewayIngress(cfg, g, gws, committed)...)
	}
	return files, Report{BlindServices: blind, RouteBlindServices: routeBlind}, nil
}

// classify returns the short class of a route's parentRef gateway, or "" when it is not a
// discovered ingress gateway — such gateways are excluded from derivation.
func classify(gws map[string]discovery.IngressGateway, gateway string) string {
	return gws[gateway].Class
}

// blindIngressServices lists the ingress backends ("ns/service", sorted, unique) whose serving
// workload is not visible in the repo — rendered by Helm or an operator. These are the backends
// whose non-routed (container) ports the repo cannot describe, so the health-prober allowance
// preserves rather than prunes while any remain unresolved.
func blindIngressServices(g *discovery.RouteGraph, gws map[string]discovery.IngressGateway) []string {
	return uniqueBackendServices(g, gws, func(b discovery.Backend) bool {
		return !g.ServiceHasVisibleWorkload(b.BackendNS, b.Service)
	})
}

// routeBlindServices lists the ingress backends whose routed port could not be resolved from the
// repo (Service absent, or a named targetPort with no EndpointSlice) — the port is a fallback
// guess. The route-derived policies covering these backends preserve rather than prune.
func routeBlindServices(g *discovery.RouteGraph, gws map[string]discovery.IngressGateway) []string {
	return uniqueBackendServices(g, gws, func(b discovery.Backend) bool { return !b.Resolved })
}

// uniqueBackendServices returns the sorted, unique "ns/service" of classified ingress backends
// matching pred.
func uniqueBackendServices(g *discovery.RouteGraph, gws map[string]discovery.IngressGateway, pred func(discovery.Backend) bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range g.Backends {
		if classify(gws, b.Gateway) == "" || !pred(b) {
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

// ── Cilium: ingress-backend contract ─────────────────────────────────────────
// Every resolved backend port reachable through the ingress gateways, cluster-wide.

func ciliumContract(cfg config.Config, g *discovery.RouteGraph, gws map[string]discovery.IngressGateway, committed CommittedReader) File {
	path := cfg.Outputs.Contract.Path
	var ports []int
	blind := false
	for _, b := range g.Backends {
		if classify(gws, b.Gateway) == "" {
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
	return File{
		Path: path,
		Content: []byte(fmt.Sprintf(ciliumContractHeader, cfg.Outputs.Contract.Name, cfg.Labels.Ingress, cfg.Labels.Ingress) +
			ciliumContractSources(gws, cfg.Gateways.PodSelector) + ciliumToPorts + ciliumPortBlock(ports)),
	}
}

// ciliumContractSources renders the contract's fromEndpoints. With a pod selector: one block per
// discovered gateway namespace selecting those labels. Without: one block per gateway selecting
// its pods by gateway name. Sorted for deterministic output.
func ciliumContractSources(gws map[string]discovery.IngressGateway, podSelector map[string]string) string {
	var b strings.Builder
	if len(podSelector) == 0 {
		list := make([]discovery.IngressGateway, 0, len(gws))
		for _, gw := range gws {
			list = append(list, gw)
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i].Namespace+"/"+list[i].Name < list[j].Namespace+"/"+list[j].Name
		})
		for _, gw := range list {
			fmt.Fprintf(&b, "        # %s\n", gw.Name)
			writeMatchLabels(&b, gw.Namespace, map[string]string{gatewayNameLabel: gw.Name})
		}
		return b.String()
	}
	byNS := map[string][]string{}
	for _, gw := range gws {
		byNS[gw.Namespace] = append(byNS[gw.Namespace], gw.Name)
	}
	nss := make([]string, 0, len(byNS))
	for ns := range byNS {
		nss = append(nss, ns)
	}
	sort.Strings(nss)
	for _, ns := range nss {
		names := byNS[ns]
		sort.Strings(names)
		fmt.Fprintf(&b, "        # %s\n", strings.Join(names, ", "))
		writeMatchLabels(&b, ns, podSelector)
	}
	return b.String()
}

// writeMatchLabels renders one fromEndpoints matchLabels block: the namespace, then the labels
// sorted by key.
func writeMatchLabels(b *strings.Builder, ns string, labels map[string]string) {
	b.WriteString("        - matchLabels:\n")
	fmt.Fprintf(b, "            k8s:io.kubernetes.pod.namespace: %s\n", ns)
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "            %s: %s\n", k, labels[k])
	}
}

// ── Cilium: health-prober allowance ──────────────────────────────────────────
// Ports the prober reaches: annotated-service targetPorts + probe-labelled workload containerPorts.

func ciliumHealthcheck(cfg config.Config, g *discovery.RouteGraph, gws map[string]discovery.IngressGateway, blind []string, committed CommittedReader) (File, error) {
	hc := cfg.Outputs.Healthcheck
	path := hc.Path

	prober := hc.Prober.MatchLabels
	proberNS := g.WorkloadNamespaces(prober)
	if len(prober) == 0 || len(proberNS) == 0 {
		return File{}, fmt.Errorf("healthcheck: no workload matches prober selector %v; set it, or disable the healthcheck output", prober)
	}

	var ports []int
	// (a) Ports of annotated services, resolved to their pod targetPorts.
	for _, s := range g.Objects.Services {
		ann := s.Metadata.Annotations
		_, hasEndpoint := ann[hc.ServiceAnnotations.Endpoint]
		if ann[hc.ServiceAnnotations.Enabled] != "true" && !hasEndpoint {
			continue
		}
		for _, sp := range s.Spec.Ports {
			ports = append(ports, g.ResolvePort(s.Metadata.Namespace, s.Metadata.Name, sp.Port))
		}
	}
	// (b) Container ports of probe-labelled workloads, plus ingress-labelled ones: an admission
	// policy commonly stamps the probe label onto ingress workloads, which the repo never shows.
	ports = append(ports, g.WorkloadsWithPodLabel(cfg.Labels.Probe, "true")...)
	ports = append(ports, g.WorkloadsWithPodLabel(cfg.Labels.Ingress, "true")...)
	// (c) For ingress backends whose workload is invisible (Helm/operator), we can't read pod
	// labels or container ports; contribute at least the resolved routed port, since a route
	// backend behind an ingress gateway is probe-eligible. Non-routed ports are covered by the
	// preserve gate below.
	for _, b := range g.Backends {
		if classify(gws, b.Gateway) == "" || g.ServiceHasVisibleWorkload(b.BackendNS, b.Service) {
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

	var src strings.Builder
	for _, ns := range proberNS {
		writeMatchLabels(&src, ns, prober)
	}
	return File{
		Path: path,
		Content: []byte(fmt.Sprintf(ciliumHealthcheckHeader, hc.Name, cfg.Labels.Probe) +
			src.String() + ciliumToPorts + ciliumPortBlock(ports)),
	}, nil
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

func istioGatewayIngress(cfg config.Config, g *discovery.RouteGraph, gws map[string]discovery.IngressGateway, committed CommittedReader) []File {
	out := cfg.Outputs.GatewayIngress
	// class → discovered gateway, for resolving each policy's SA principal from the manifest.
	byClass := map[string]discovery.IngressGateway{}
	for _, gw := range gws {
		byClass[gw.Class] = gw
	}

	type key struct{ ns, class string }
	type group struct {
		ports []int
		blind bool // some backend in this group has an unresolved (guessed) port
	}
	groups := map[key]*group{}
	for _, b := range g.Backends {
		c := classify(gws, b.Gateway)
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

	keys := make([]key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].ns+"|"+keys[i].class < keys[j].ns+"|"+keys[j].class
	})

	var files []File
	for _, k := range keys {
		gr := groups[k]
		gw := byClass[k.class]
		name := out.Name + "-" + k.class
		path := fmt.Sprintf("%s/%s/%s.yaml", out.Dir, k.ns, name)
		ports := gr.ports
		// Honesty gate: if any backend for this namespace routed to a guessed port, preserve the
		// committed policy's ports rather than prune against the guess.
		if gr.blind {
			if prev, err := committed(path); err == nil {
				ports = append(ports, extractPorts(prev)...)
			}
		}
		files = append(files, File{
			Path: path,
			Content: []byte(fmt.Sprintf(istioTemplate,
				name,
				cfg.Labels.Ingress,
				gw.Namespace, gw.ServiceAccount,
				istioPortList(discovery.SortDedupInts(ports)),
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
