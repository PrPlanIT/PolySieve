// Command polysieve derives Kubernetes network authorization policy from a GitOps
// repository's own manifests (the source of truth), rather than from a live cluster.
package main

import "github.com/PrPlanIT/PolySieve/internal/cli"

func main() {
	cli.Execute()
}
