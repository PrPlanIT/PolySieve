// Package discovery is the generic, cluster-agnostic half of PolySieve: it turns a set of
// rendered Kubernetes objects into a RouteGraph — the resolved (gateway, backend-namespace,
// backend-port) tuples a policy can be derived from. It has no knowledge of any particular
// cluster's label model, CRDs, or gateway identities; that lives in a profile.
package discovery

import (
	"sort"

	"github.com/PrPlanIT/PolySieve/internal/kube"
)

// Backend is one resolved edge of the route graph: a gateway routes to a Service in a
// namespace, and that Service's declared port resolves to a concrete backend (pod) port.
type Backend struct {
	RouteNS     string // namespace of the HTTPRoute
	Gateway     string // parentRef name, verbatim (e.g. "xylem-gateway")
	BackendNS   string // backendRef.namespace, or RouteNS when absent
	Service     string // backendRef.name
	ServicePort int    // backendRef.port (the Service port)
	Port        int    // resolved targetPort (the pod containerPort)
	Resolved    bool   // true if Port is a real resolution; false if a fallback guess (the
	// Service is absent from the repo, or its targetPort is named with no EndpointSlice to
	// resolve it — i.e. the pods are rendered outside the repo). A false here is the route-layer
	// counterpart of an invisible workload: the port cannot be trusted, so the honesty gate must
	// not prune against it.
}

// RouteGraph is the discovered ingress graph plus the object index used to resolve ports.
// Profiles read Backends for policy derivation and use the index for auxiliary lookups
// (e.g. annotation- or label-driven probe ports).
type RouteGraph struct {
	Backends []Backend
	Objects  *kube.Objects

	svcIndex   map[string]kube.Service        // "ns/name" → Service (first occurrence wins)
	sliceIndex map[string][]kube.EndpointSlice // "ns/serviceName" → slices
}

// Build resolves every HTTPRoute backendRef into a Backend edge. Order follows the input
// object order (as `kustomize build` emits it), matching the reference generators'
// first-match semantics for deterministic output.
func Build(objs *kube.Objects) *RouteGraph {
	g := &RouteGraph{
		Objects:    objs,
		svcIndex:   make(map[string]kube.Service, len(objs.Services)),
		sliceIndex: make(map[string][]kube.EndpointSlice),
	}
	for _, s := range objs.Services {
		key := s.Metadata.Namespace + "/" + s.Metadata.Name
		if _, seen := g.svcIndex[key]; !seen {
			g.svcIndex[key] = s
		}
	}
	for _, e := range objs.EndpointSlices {
		key := e.Metadata.Namespace + "/" + e.ServiceName()
		g.sliceIndex[key] = append(g.sliceIndex[key], e)
	}

	for _, r := range objs.HTTPRoutes {
		routeNS := r.Metadata.Namespace
		for _, p := range r.Spec.ParentRefs {
			for _, rule := range r.Spec.Rules {
				for _, b := range rule.BackendRefs {
					if b.Port == nil {
						continue // backendRef with no port is dropped (matches jq select(.port != null))
					}
					backendNS := b.Namespace
					if backendNS == "" {
						backendNS = routeNS
					}
					port, resolved := g.ResolvePortEx(backendNS, b.Name, *b.Port)
					g.Backends = append(g.Backends, Backend{
						RouteNS:     routeNS,
						Gateway:     p.Name,
						BackendNS:   backendNS,
						Service:     b.Name,
						ServicePort: *b.Port,
						Port:        port,
						Resolved:    resolved,
					})
				}
			}
		}
	}
	return g
}

// ResolvePort maps a (namespace, service, service-port) to the concrete backend port; see
// ResolvePortEx for the resolution rules. The boolean (did-we-resolve-or-guess) is dropped.
func (g *RouteGraph) ResolvePort(ns, svc string, svcPort int) int {
	p, _ := g.ResolvePortEx(ns, svc, svcPort)
	return p
}

// ResolvePortEx maps a (namespace, service, service-port) to the concrete backend port and
// reports whether the value is a real resolution (true) or a fallback guess (false), mirroring
// the reference resolver exactly:
//   Service.spec.ports[port==svcPort].targetPort // .port; numeric → use it; omitted → the
//   service port (defaults to it); named → the EndpointSlice port of that name.
// It falls back to the service port — and reports false — when the Service is absent from the
// repo, no port entry matches, or a named targetPort has no EndpointSlice to resolve it. Those
// are exactly the cases where the pods are rendered outside the repo, so the "port" is a guess.
func (g *RouteGraph) ResolvePortEx(ns, svc string, svcPort int) (int, bool) {
	s, ok := g.svcIndex[ns+"/"+svc]
	if !ok {
		return svcPort, false // service not in the repo → guess
	}
	var tp *kube.IntOrString
	for i := range s.Spec.Ports {
		if s.Spec.Ports[i].Port == svcPort {
			tp = &s.Spec.Ports[i].TargetPort
			break // first match wins
		}
	}
	if tp == nil {
		return svcPort, false // no port entry matches the service port → guess
	}
	if !tp.Present {
		return svcPort, true // targetPort omitted → k8s defaults it to the service port
	}
	if tp.IsInt {
		return tp.Int, true
	}
	// Named targetPort → resolve via EndpointSlice ports of that name, then via the backing
	// workload's named container port (repo-native: the pod template names its ports, so a raw
	// workload in the repo resolves without a runtime EndpointSlice).
	for _, e := range g.sliceIndex[ns+"/"+svc] {
		for _, ep := range e.Ports {
			if ep.Name == tp.Str {
				return ep.Port, true
			}
		}
	}
	if p, ok := g.workloadNamedPort(ns, s.Spec.Selector, tp.Str); ok {
		return p, true
	}
	return svcPort, false // named port, no EndpointSlice and no visible workload → guess
}

// workloadNamedPort returns the containerPort of a named port on a repo workload backing the
// given selector in ns (used to resolve a Service's named targetPort without an EndpointSlice).
func (g *RouteGraph) workloadNamedPort(ns string, selector map[string]string, name string) (int, bool) {
	if len(selector) == 0 {
		return 0, false
	}
	for _, w := range g.Objects.Workloads {
		if w.Metadata.Namespace != ns || !labelsSatisfy(w.Spec.Template.Metadata.Labels, selector) {
			continue
		}
		for _, c := range w.Spec.Template.Spec.Containers {
			for _, cp := range c.Ports {
				if cp.Name == name {
					return cp.ContainerPort, true
				}
			}
		}
	}
	return 0, false
}

// WorkloadsWithPodLabel returns the container ports (TCP or unset protocol) of every
// workload whose pod template carries label==value. Used by profiles for label-driven
// probe-port derivation; generic mechanism, no cluster-specific meaning here.
func (g *RouteGraph) WorkloadsWithPodLabel(label, value string) []int {
	var ports []int
	for _, w := range g.Objects.Workloads {
		if w.Spec.Template.Metadata.Labels[label] != value {
			continue
		}
		for _, c := range w.Spec.Template.Spec.Containers {
			for _, cp := range c.Ports {
				if cp.Protocol == "TCP" || cp.Protocol == "" {
					ports = append(ports, cp.ContainerPort)
				}
			}
		}
	}
	return ports
}

// ServiceHasVisibleWorkload reports whether the repo itself contains a workload that backs the
// given service — a Deployment/StatefulSet/DaemonSet in the same namespace whose pod-template
// labels satisfy the service's selector. This is the coverage primitive the honesty gate is
// built on:
//   - selector satisfied by a repo workload → visible: its container ports are derivable;
//   - selector present but unmatched → blind: the pods are rendered outside the repo (a Helm
//     chart or an operator), so their non-routed ports cannot be known from the repo;
//   - no selector (external / manually-endpointed service) → treated as visible: it isn't
//     workload-backed, and what ports it exposes is declared on the Service/EndpointSlice.
// A service absent from the repo entirely is blind (nothing to see).
func (g *RouteGraph) ServiceHasVisibleWorkload(ns, svc string) bool {
	s, ok := g.svcIndex[ns+"/"+svc]
	if !ok {
		return false
	}
	if len(s.Spec.Selector) == 0 {
		return true
	}
	for _, w := range g.Objects.Workloads {
		if w.Metadata.Namespace != ns {
			continue
		}
		if labelsSatisfy(w.Spec.Template.Metadata.Labels, s.Spec.Selector) {
			return true
		}
	}
	return false
}

// labelsSatisfy reports whether every want[k]=v is present in have (label-selector semantics).
func labelsSatisfy(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

// ServicesWithAnnotation returns services carrying any of the given annotation keys with a
// non-empty value. Generic mechanism used by profiles (e.g. gatus endpoint annotations).
func (g *RouteGraph) ServicesWithAnnotation(keys ...string) []kube.Service {
	var out []kube.Service
	for _, s := range g.Objects.Services {
		for _, k := range keys {
			if v, ok := s.Metadata.Annotations[k]; ok && v != "" {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// SortDedupInts returns a numerically-ascending, de-duplicated copy (the `sort -un` the
// reference generators apply to every port list).
func SortDedupInts(in []int) []int {
	seen := make(map[int]struct{}, len(in))
	out := make([]int, 0, len(in))
	for _, v := range in {
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}
