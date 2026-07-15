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

// Profile renders a RouteGraph into a cluster's policy files.
type Profile interface {
	Name() string
	// Roots are the repo-relative kustomize build directories whose rendered manifests feed
	// discovery (the HTTPRoutes / Services / EndpointSlices / workloads to reason over).
	Roots() []string
	Render(g *discovery.RouteGraph) ([]File, error)
}
