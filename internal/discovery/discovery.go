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
					g.Backends = append(g.Backends, Backend{
						RouteNS:     routeNS,
						Gateway:     p.Name,
						BackendNS:   backendNS,
						Service:     b.Name,
						ServicePort: *b.Port,
						Port:        g.ResolvePort(backendNS, b.Name, *b.Port),
					})
				}
			}
		}
	}
	return g
}

// ResolvePort maps a (namespace, service, service-port) to the concrete backend port,
// mirroring the reference resolver exactly:
//   Service.spec.ports[port==svcPort].targetPort // .port; numeric → use it; named → the
//   EndpointSlice port of that name; any miss → fall back to the service port.
func (g *RouteGraph) ResolvePort(ns, svc string, svcPort int) int {
	s, ok := g.svcIndex[ns+"/"+svc]
	if !ok {
		return svcPort
	}
	var tp *kube.IntOrString
	for i := range s.Spec.Ports {
		if s.Spec.Ports[i].Port == svcPort {
			tp = &s.Spec.Ports[i].TargetPort
			break // first match wins
		}
	}
	if tp == nil || !tp.Present {
		return svcPort // no matching port entry, or no targetPort → the service port
	}
	if tp.IsInt {
		return tp.Int
	}
	// Named targetPort → resolve via EndpointSlice ports of that name.
	for _, e := range g.sliceIndex[ns+"/"+svc] {
		for _, ep := range e.Ports {
			if ep.Name == tp.Str {
				return ep.Port
			}
		}
	}
	return svcPort // named port unresolved → fall back
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
