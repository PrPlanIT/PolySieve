// Package cli wires PolySieve's cobra command tree.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	flagRepo      string
	flagConfig    string
	flagKustomize string
	flagCluster   bool
	flagKubectl   string
	flagHelm      bool
	flagHelmBin   string
	flagVerbose   bool
	flagDisable   []string
)

var rootCmd = &cobra.Command{
	Use:   "polysieve",
	Short: "Derive Kubernetes network policy from a GitOps repository",
	Long: `PolySieve reads a GitOps repository's own manifests (the source of truth) and
derives the least-privilege network authorization policy each namespace needs. Build roots,
ingress gateways, the health prober and output locations are derived from the repo; a config
file (--config) or flags override them.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVar(&flagRepo, "repo", ".", "path to the GitOps repository root")
	pf.StringVar(&flagConfig, "config", "", "config file (default <repo>/.polysieve.yml when present)")
	pf.StringVar(&flagKustomize, "kustomize", "kustomize", "kustomize binary to render with")
	pf.BoolVar(&flagCluster, "cluster", false, "best-effort: resolve blind (Helm/operator) backends from the live cluster when reachable")
	pf.StringVar(&flagKubectl, "kubectl", "kubectl", "kubectl binary for --cluster augmentation")
	pf.BoolVar(&flagHelm, "helm", false, "best-effort: render HelmReleases with `helm template` to resolve Helm-backed backends (needs chart-registry network)")
	pf.StringVar(&flagHelmBin, "helm-bin", "helm", "helm binary for --helm rendering")
	pf.BoolVarP(&flagVerbose, "verbose", "v", false, "verbose output")

	pf.StringSliceVar(&over.Roots, "root", nil, "kustomize build root (repeatable; default: derived from the repo's Flux Kustomizations)")
	pf.StringVar(&over.Labels.Ingress, "ingress-label", "", "pod label marking ingress backends")
	pf.StringVar(&over.Labels.Probe, "probe-label", "", "pod label marking pods the health prober may reach")
	pf.StringToStringVar(&over.Gateways.Selector, "gateway-selector", nil, "Gateway infrastructure labels selecting ingress gateways (default: Istio-managed Gateways)")
	pf.StringVar(&over.Gateways.InstanceLabel, "gateway-instance-label", "", "Gateway infrastructure label holding the instance name")
	pf.StringToStringVar(&over.Gateways.PodSelector, "gateway-pod-selector", nil, "labels selecting gateway pods as the contract source (default: per-gateway gateway-name label)")
	pf.StringToStringVar(&over.Outputs.Healthcheck.Prober.MatchLabels, "prober-selector", nil, "pod labels identifying the health prober")
	pf.StringVar(&over.Outputs.Contract.Name, "contract-name", "", "name of the ingress-backend contract policy")
	pf.StringVar(&over.Outputs.Contract.Path, "contract-path", "", "repo-relative path of the ingress-backend contract policy")
	pf.StringVar(&over.Outputs.Healthcheck.Name, "healthcheck-name", "", "name of the health-prober policy")
	pf.StringVar(&over.Outputs.Healthcheck.Path, "healthcheck-path", "", "repo-relative path of the health-prober policy")
	pf.StringVar(&over.Outputs.GatewayIngress.Name, "gateway-ingress-name", "", "name prefix of the per-namespace gateway-ingress policies")
	pf.StringVar(&over.Outputs.GatewayIngress.Dir, "gateway-ingress-dir", "", "repo-relative directory of the per-namespace gateway-ingress policies")
	pf.StringSliceVar(&flagDisable, "disable", nil, "outputs to skip: contract, healthcheck, gateway-ingress")
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
