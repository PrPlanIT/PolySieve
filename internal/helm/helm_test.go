package helm

import (
	"strings"
	"testing"

	"github.com/PrPlanIT/PolySieve/internal/kube"
)

func TestChartLocation(t *testing.T) {
	classic := kube.HelmRepository{}
	classic.Metadata.Namespace = "flux-system"
	classic.Metadata.Name = "grafana"
	classic.Spec.URL = "https://grafana.github.io/helm-charts"

	ociRepoAsHelm := kube.HelmRepository{}
	ociRepoAsHelm.Metadata.Namespace = "flux-system"
	ociRepoAsHelm.Metadata.Name = "harbor"
	ociRepoAsHelm.Spec.URL = "oci://registry-1.docker.io/bitnamicharts"
	ociRepoAsHelm.Spec.Type = "oci"

	ociRef := kube.OCIRepository{}
	ociRef.Metadata.Namespace = "flux-system"
	ociRef.Metadata.Name = "podinfo"
	ociRef.Spec.URL = "oci://ghcr.io/stefanprodan/charts/podinfo"
	ociRef.Spec.Ref.Tag = "6.7.0"

	helmRepos := map[string]kube.HelmRepository{
		"flux-system/grafana": classic,
		"flux-system/harbor":  ociRepoAsHelm,
	}
	ociRepos := map[string]kube.OCIRepository{"flux-system/podinfo": ociRef}

	mkHR := func(ns string) kube.HelmRelease {
		var hr kube.HelmRelease
		hr.Metadata.Namespace = ns
		return hr
	}

	// Classic HelmRepository → chart name + --repo url + --version.
	hr := mkHR("gossip-stone")
	hr.Spec.Chart.Spec.Chart = "grafana"
	hr.Spec.Chart.Spec.Version = "8.5.1"
	hr.Spec.Chart.Spec.SourceRef = kube.SourceRef{Kind: "HelmRepository", Name: "grafana", Namespace: "flux-system"}
	got, err := chartLocation(hr, helmRepos, ociRepos)
	if err != nil || strings.Join(got, " ") != "grafana --repo https://grafana.github.io/helm-charts --version 8.5.1" {
		t.Errorf("classic: got %v err %v", got, err)
	}

	// OCI-typed HelmRepository → url/chart + --version.
	hr = mkHR("hyrule-castle")
	hr.Spec.Chart.Spec.Chart = "harbor"
	hr.Spec.Chart.Spec.Version = "1.15.0"
	hr.Spec.Chart.Spec.SourceRef = kube.SourceRef{Kind: "HelmRepository", Name: "harbor", Namespace: "flux-system"}
	got, err = chartLocation(hr, helmRepos, ociRepos)
	if err != nil || strings.Join(got, " ") != "oci://registry-1.docker.io/bitnamicharts/harbor --version 1.15.0" {
		t.Errorf("oci-helmrepo: got %v err %v", got, err)
	}

	// chartRef → OCIRepository, tag wins as the version.
	hr = mkHR("tingle-tuner")
	hr.Spec.ChartRef = kube.SourceRef{Kind: "OCIRepository", Name: "podinfo", Namespace: "flux-system"}
	got, err = chartLocation(hr, helmRepos, ociRepos)
	if err != nil || strings.Join(got, " ") != "oci://ghcr.io/stefanprodan/charts/podinfo --version 6.7.0" {
		t.Errorf("chartRef-oci: got %v err %v", got, err)
	}

	// Unknown source → error, never a silent empty chart.
	hr = mkHR("x")
	hr.Spec.Chart.Spec.SourceRef = kube.SourceRef{Kind: "GitRepository", Name: "y"}
	if _, err := chartLocation(hr, helmRepos, ociRepos); err == nil {
		t.Error("GitRepository source should error")
	}
}
