package discovery

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PrPlanIT/PolySieve/internal/kube"
)

// FluxRoots derives the build roots from the repo's own Flux Kustomizations. The seed is the
// self-syncing Kustomization `flux bootstrap` writes (its path renders a Kustomization with that
// same path); the roots are every path reachable from it through rendered Kustomizations that
// share its source. Kustomizations not reachable from the seed (unreferenced or retired files)
// are ignored.
func FluxRoots(ctx context.Context, kustomizeBin, repoDir string) ([]string, error) {
	candidates, err := scanFluxKustomizations(repoDir)
	if err != nil {
		return nil, err
	}
	type rendered struct {
		ks  []kube.FluxKustomization
		err error
	}
	cache := map[string]*rendered{}
	render := func(p string) *rendered {
		if r, ok := cache[p]; ok {
			return r
		}
		r := &rendered{}
		out, err := kustomizeBuild(ctx, kustomizeBin, filepath.Join(repoDir, p))
		if err == nil {
			var objs kube.Objects
			err = kube.Parse(&objs, out)
			r.ks = objs.FluxKustomizations
		}
		r.err = err
		cache[p] = r
		return r
	}

	var seed *kube.FluxKustomization
	for i := range candidates {
		c := &candidates[i]
		p := cleanPath(c.Spec.Path)
		if !isDir(repoDir, p) {
			continue
		}
		r := render(p)
		if r.err != nil {
			continue
		}
		for _, k := range r.ks {
			if cleanPath(k.Spec.Path) == p && k.Metadata.Name == c.Metadata.Name {
				s := k // the rendered copy carries any namespace the build sets
				seed = &s
				break
			}
		}
		if seed != nil {
			break
		}
	}
	if seed == nil {
		return nil, fmt.Errorf("no self-syncing Flux Kustomization found in %s; pass --root", repoDir)
	}
	source := sourceKey(*seed)

	seen := map[string]bool{}
	queue := []string{cleanPath(seed.Spec.Path)}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if seen[p] {
			continue
		}
		seen[p] = true
		r := render(p)
		if r.err != nil {
			return nil, fmt.Errorf("rendering %s: %w", p, r.err)
		}
		for _, k := range r.ks {
			if sourceKey(k) == source {
				queue = append(queue, cleanPath(k.Spec.Path))
			}
		}
	}
	roots := make([]string, 0, len(seen))
	for p := range seen {
		roots = append(roots, p)
	}
	sort.Strings(roots)
	return roots, nil
}

// scanFluxKustomizations reads every YAML file in the repo (outside dot-directories) for Flux
// Kustomization documents. Files that don't parse are skipped.
func scanFluxKustomizations(repoDir string) ([]kube.FluxKustomization, error) {
	var out []kube.FluxKustomization
	err := filepath.WalkDir(repoDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != repoDir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(path); ext != ".yaml" && ext != ".yml" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil || !bytes.Contains(b, []byte("kustomize.toolkit.fluxcd.io/")) {
			return nil
		}
		var objs kube.Objects
		if kube.Parse(&objs, b) == nil {
			out = append(out, objs.FluxKustomizations...)
		}
		return nil
	})
	return out, err
}

func sourceKey(k kube.FluxKustomization) string {
	ns := k.Spec.SourceRef.Namespace
	if ns == "" {
		ns = k.Metadata.Namespace
	}
	return k.Spec.SourceRef.Kind + "/" + ns + "/" + k.Spec.SourceRef.Name
}

func cleanPath(p string) string {
	return filepath.Clean(strings.TrimPrefix(p, "/"))
}

func isDir(repoDir, p string) bool {
	st, err := os.Stat(filepath.Join(repoDir, p))
	return err == nil && st.IsDir()
}
