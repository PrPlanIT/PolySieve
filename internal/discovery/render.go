package discovery

import (
	"bytes"
	"context"
	"fmt"
	"os"
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

// kustomizeBuild renders dir. A directory without a kustomization file is rendered as Flux does:
// its YAML files and subdirectories, recursively, a subdirectory with a kustomization file built.
func kustomizeBuild(ctx context.Context, bin, dir string) ([]byte, error) {
	if hasKustomization(dir) {
		return kustomizeRun(ctx, bin, dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		var b []byte
		switch {
		case e.IsDir():
			b, err = kustomizeBuild(ctx, bin, p)
		case filepath.Ext(p) == ".yaml" || filepath.Ext(p) == ".yml":
			b, err = os.ReadFile(p)
		default:
			continue
		}
		if err != nil {
			return nil, err
		}
		out.WriteString("---\n")
		out.Write(b)
		out.WriteString("\n")
	}
	return out.Bytes(), nil
}

func hasKustomization(dir string) bool {
	for _, n := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return true
		}
	}
	return false
}

func kustomizeRun(ctx context.Context, bin, dir string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, "build", dir, "--load-restrictor=LoadRestrictionsNone")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("kustomize build %s: %w: %s", dir, err, stderr.String())
	}
	return stdout.Bytes(), nil
}
