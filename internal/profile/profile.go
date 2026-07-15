// Package profile defines the project-specific rendering half of PolySieve: a profile maps
// the generic RouteGraph to a particular cluster's concrete policy objects (CRDs, label
// model, gateway identities, file layout). The dungeon cluster is the first profile.
package profile

import "github.com/PrPlanIT/PolySieve/internal/discovery"

// File is one policy file to emit: a repo-relative path and its exact byte content.
type File struct {
	Path    string
	Content []byte
}

// CommittedReader returns the current committed bytes of a repo-relative path (nil, nil when
// absent). The honesty gate uses it to preserve ports a profile cannot re-derive under partial
// coverage, so a port that can't be accounted for is never silently pruned.
type CommittedReader func(path string) ([]byte, error)

// Report carries the coverage facts a run must surface so PolySieve never prunes silently.
type Report struct {
	// BlindServices are ingress backends ("ns/service") whose serving workload is not visible
	// in the repo (Helm- or operator-rendered). Their non-routed ports cannot be derived, so a
	// policy that depends on them (the Gatus healthcheck) was emitted in preserve mode.
	BlindServices []string
	// RouteBlindServices are ingress backends whose routed port could not be resolved from the
	// repo — the Service is absent or its targetPort is a named port with no EndpointSlice — so
	// the port is a fallback guess. The route-derived policies for their files were emitted in
	// preserve mode rather than pruned against a guess.
	RouteBlindServices []string
}

// Profile renders a RouteGraph into a cluster's policy files.
type Profile interface {
	Name() string
	// Roots are the repo-relative kustomize build directories whose rendered manifests feed
	// discovery (the HTTPRoutes / Services / EndpointSlices / workloads to reason over).
	Roots() []string
	// Render derives the policy files. committed lets a profile read the current on-disk policy
	// to preserve ports it cannot re-derive when coverage is partial; the returned Report names
	// the blind backends that triggered any preservation.
	Render(g *discovery.RouteGraph, committed CommittedReader) ([]File, Report, error)
}
