package discovery

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/PrPlanIT/PolySieve/internal/kube"
)

// RenderRoots renders each repo-relative kustomize directory and parses the combined
// manifest stream into a single object set. This is the repo-driven replacement for reading
// the live cluster: the source of truth is the committed manifests, rendered once.
func RenderRoots(ctx context.Context, kustomizeBin, repoDir string, roots []string) (*kube.Objects, error) {
	objs := &kube.Objects{}
	for _, root := range roots {
		out, err := kustomizeBuild(ctx, kustomizeBin, filepath.Join(repoDir, root))
		if err != nil {
			return nil, fmt.Errorf("rendering %s: %w", root, err)
		}
		if err := kube.Parse(objs, out); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", root, err)
		}
	}
	return objs, nil
}

func kustomizeBuild(ctx context.Context, bin, dir string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, "build", dir, "--load-restrictor=LoadRestrictionsNone")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("kustomize build %s: %w: %s", dir, err, stderr.String())
	}
	return stdout.Bytes(), nil
}
