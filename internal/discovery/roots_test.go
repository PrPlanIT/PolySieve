package discovery

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func ks(name, path, source string) string {
	return `---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: ` + name + `
  namespace: flux-system
spec:
  path: ` + path + `
  sourceRef:
    kind: GitRepository
    name: ` + source + `
`
}

// The roots are the paths reachable from the self-syncing Kustomization through Kustomizations of
// the same source; an unreferenced Kustomization file and one of another source are ignored.
func TestFluxRoots(t *testing.T) {
	repo := t.TempDir()
	// A stub kustomize: "build <dir>" prints <dir>/rendered.yaml.
	bin := filepath.Join(t.TempDir(), "kustomize")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat \"$2/rendered.yaml\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cluster := ks("flux-system", "./clusters/prod", "flux-system") +
		ks("apps", "./apps/prod", "flux-system") +
		ks("vendor", "./vendor", "other-repo")
	// clusters/prod has no kustomization file: rendered from its YAML files, as Flux does.
	write("clusters/prod/sync.yaml", cluster)
	for _, d := range []string{"apps/prod", "apps/extra", "vendor", "retired"} {
		write(d+"/kustomization.yaml", "")
	}
	write("apps/prod/rendered.yaml", ks("apps-extra", "./apps/extra", "flux-system"))
	write("apps/extra/rendered.yaml", "")
	write("vendor/rendered.yaml", "")
	write("retired/ks.yaml", ks("retired", "./retired", "flux-system"))
	write("retired/rendered.yaml", "")

	got, err := FluxRoots(context.Background(), bin, repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"apps/extra", "apps/prod", "clusters/prod"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("roots = %v, want %v", got, want)
	}
}
