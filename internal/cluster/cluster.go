// Package cluster augments repo-derived discovery with live-cluster ground truth. It is a
// best-effort supplement, never a dependency: when the cluster is reachable it fills in the
// objects the repo cannot express (Helm- and operator-rendered Services, EndpointSlices, and
// workloads), so blind backends resolve authoritatively; when it is unreachable the caller
// degrades to repo-only derivation and the honesty gate preserves rather than prunes.
package cluster

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/PrPlanIT/PolySieve/internal/kube"
)

// resources fetched from the cluster to resolve blind backends. HTTPRoutes are deliberately
// excluded: routing intent stays repo-authoritative — the cluster only resolves ports and the
// pods behind them, never what routes exist.
const resources = "deployments,statefulsets,daemonsets,services,endpointslices"

// Augment fetches the live-cluster objects and appends them to objs, which must already hold the
// repo objects: appended-after means repo Services win under discovery's first-occurrence-wins
// indexing, so visible backends keep resolving from the repo and the cluster only fills gaps.
// It returns an error (leaving objs untouched of any partial parse it can avoid) when the
// cluster is unreachable, so the caller can degrade to repo-only.
func Augment(ctx context.Context, kubectlBin string, objs *kube.Objects) error {
	cmd := exec.CommandContext(ctx, kubectlBin, "get", resources, "-A", "-o", "yaml")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return fmt.Errorf("kubectl get %s: %w: %s", resources, err, ee.Stderr)
		}
		return fmt.Errorf("kubectl get %s: %w", resources, err)
	}
	if err := kube.Parse(objs, out); err != nil {
		return fmt.Errorf("parsing cluster objects: %w", err)
	}
	return nil
}
