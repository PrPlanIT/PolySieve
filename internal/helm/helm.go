// Package helm is the cluster-absent middle tier: it renders Flux HelmReleases with
// `helm template` and feeds the resulting workloads into discovery, so Helm-backed backends
// resolve without a live cluster. It is best-effort and per-release — a chart that will not
// render (private repo, unsupported source, missing values) is skipped and reported, and the
// honesty gate then preserves that backend rather than pruning it. Unlike repo-only derivation
// this reaches a chart registry (network), which is the accepted trade for cluster-absent runs.
package helm

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/PrPlanIT/PolySieve/internal/kube"
)

// Summary records which HelmReleases rendered and which were skipped (with a reason).
type Summary struct {
	Rendered []string
	Skipped  []string
}

// Render renders every HelmRelease in objs and appends the resulting Deployments/StatefulSets/
// Services/EndpointSlices back into objs. Repo objects were parsed first, so they stay
// authoritative; rendered objects only fill gaps. Individual failures are collected in the
// Summary, never returned as a hard error — a HelmRelease that won't render stays blind and the
// honesty gate preserves it.
func Render(ctx context.Context, helmBin string, objs *kube.Objects) Summary {
	helmRepos := map[string]kube.HelmRepository{}
	for _, r := range objs.HelmRepositories {
		helmRepos[r.Metadata.Namespace+"/"+r.Metadata.Name] = r
	}
	ociRepos := map[string]kube.OCIRepository{}
	for _, r := range objs.OCIRepositories {
		ociRepos[r.Metadata.Namespace+"/"+r.Metadata.Name] = r
	}

	// Iterate a snapshot: Render appends to objs.Workloads/Services, not to the slices ranged.
	releases := objs.HelmReleases
	var sum Summary
	for _, hr := range releases {
		id := hr.Metadata.Namespace + "/" + hr.Metadata.Name
		out, err := renderOne(ctx, helmBin, hr, helmRepos, ociRepos)
		if err != nil {
			sum.Skipped = append(sum.Skipped, id+": "+err.Error())
			continue
		}
		if err := kube.Parse(objs, out); err != nil {
			sum.Skipped = append(sum.Skipped, id+": parsing rendered chart: "+err.Error())
			continue
		}
		sum.Rendered = append(sum.Rendered, id)
	}
	return sum
}

func renderOne(ctx context.Context, helmBin string, hr kube.HelmRelease, helmRepos map[string]kube.HelmRepository, ociRepos map[string]kube.OCIRepository) ([]byte, error) {
	ns := hr.Spec.TargetNamespace
	if ns == "" {
		ns = hr.Metadata.Namespace
	}
	release := hr.Spec.ReleaseName
	if release == "" {
		release = hr.Metadata.Name
	}

	args := []string{"template", release}
	chartArgs, err := chartLocation(hr, helmRepos, ociRepos)
	if err != nil {
		return nil, err
	}
	args = append(args, chartArgs...)
	args = append(args, "--namespace", ns, "--include-crds=false", "--skip-tests", "--no-hooks")

	if valuesFile, cleanup, err := writeValues(hr); err != nil {
		return nil, err
	} else if valuesFile != "" {
		defer cleanup()
		args = append(args, "-f", valuesFile)
	}

	out, err := exec.CommandContext(ctx, helmBin, args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("helm template: %s", firstLine(ee.Stderr))
		}
		return nil, fmt.Errorf("helm template: %w", err)
	}
	return out, nil
}

// chartLocation returns the `helm template` args that name the chart, from either the classic
// chartTemplate (HelmRepository + chart name) or a chartRef/sourceRef to an OCIRepository.
func chartLocation(hr kube.HelmRelease, helmRepos map[string]kube.HelmRepository, ociRepos map[string]kube.OCIRepository) ([]string, error) {
	c := hr.Spec.Chart.Spec

	// chartRef → OCIRepository (Flux v2 chartRef form).
	if hr.Spec.ChartRef.Kind == "OCIRepository" {
		oci, ok := ociRepos[srcNS(hr, hr.Spec.ChartRef.Namespace)+"/"+hr.Spec.ChartRef.Name]
		if !ok {
			return nil, fmt.Errorf("OCIRepository %q not found", hr.Spec.ChartRef.Name)
		}
		return ociArgs(oci, c.Version), nil
	}

	switch c.SourceRef.Kind {
	case "HelmRepository":
		repo, ok := helmRepos[srcNS(hr, c.SourceRef.Namespace)+"/"+c.SourceRef.Name]
		if !ok {
			return nil, fmt.Errorf("HelmRepository %q not found", c.SourceRef.Name)
		}
		if repo.Spec.Type == "oci" || strings.HasPrefix(repo.Spec.URL, "oci://") {
			loc := strings.TrimSuffix(repo.Spec.URL, "/") + "/" + c.Chart
			return withVersion([]string{loc}, c.Version), nil
		}
		return withVersion([]string{c.Chart, "--repo", repo.Spec.URL}, c.Version), nil
	case "OCIRepository":
		oci, ok := ociRepos[srcNS(hr, c.SourceRef.Namespace)+"/"+c.SourceRef.Name]
		if !ok {
			return nil, fmt.Errorf("OCIRepository %q not found", c.SourceRef.Name)
		}
		return ociArgs(oci, c.Version), nil
	case "GitRepository":
		return nil, fmt.Errorf("GitRepository chart source not supported")
	}
	return nil, fmt.Errorf("no resolvable chart source")
}

func ociArgs(oci kube.OCIRepository, fallbackVersion string) []string {
	v := oci.Spec.Ref.Tag
	if v == "" {
		v = fallbackVersion
	}
	return withVersion([]string{oci.Spec.URL}, v)
}

func withVersion(args []string, version string) []string {
	if version != "" {
		args = append(args, "--version", version)
	}
	return args
}

// srcNS defaults an optional sourceRef namespace to the HelmRelease's own namespace.
func srcNS(hr kube.HelmRelease, ns string) string {
	if ns != "" {
		return ns
	}
	return hr.Metadata.Namespace
}

// writeValues marshals a HelmRelease's inline spec.values to a temp file, returning "" when
// there are no values. valuesFrom (ConfigMap/Secret refs) is intentionally ignored — port
// topology almost never depends on it, and it can require cluster/secret access.
func writeValues(hr kube.HelmRelease) (path string, cleanup func(), err error) {
	if hr.Spec.Values.Kind == 0 || (hr.Spec.Values.Kind == yaml.MappingNode && len(hr.Spec.Values.Content) == 0) {
		return "", func() {}, nil
	}
	b, err := yaml.Marshal(&hr.Spec.Values)
	if err != nil {
		return "", func() {}, fmt.Errorf("marshaling values: %w", err)
	}
	f, err := os.CreateTemp("", "polysieve-values-*.yaml")
	if err != nil {
		return "", func() {}, err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", func() {}, err
	}
	f.Close()
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
