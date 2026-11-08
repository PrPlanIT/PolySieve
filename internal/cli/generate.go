package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/PrPlanIT/PolySieve/internal/cluster"
	"github.com/PrPlanIT/PolySieve/internal/config"
	"github.com/PrPlanIT/PolySieve/internal/discovery"
	"github.com/PrPlanIT/PolySieve/internal/helm"
	"github.com/PrPlanIT/PolySieve/internal/policy"
)

// over holds the settings given as flags; they override the config file.
var over config.Config

var generateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Render the repo and write derived policy files",
	RunE:  func(cmd *cobra.Command, args []string) error { return run(cmd, true) },
}

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "Derive policy and diff against committed files (nonzero exit on drift)",
	RunE:  func(cmd *cobra.Command, args []string) error { return run(cmd, false) },
}

func init() {
	rootCmd.AddCommand(generateCmd)
	rootCmd.AddCommand(checkCmd)
}

func run(cmd *cobra.Command, write bool) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	roots := cfg.Roots
	if len(roots) == 0 {
		if roots, err = discovery.FluxRoots(cmd.Context(), flagKustomize, flagRepo); err != nil {
			return err
		}
	}
	if err := policy.Locate(flagRepo, &cfg); err != nil {
		return err
	}

	objs, err := discovery.RenderRoots(cmd.Context(), flagKustomize, flagRepo, roots)
	if err != nil {
		return err
	}
	if flagCluster {
		// Best-effort: append live-cluster objects so Helm/operator backends resolve. Repo
		// objects were parsed first, so they stay authoritative; the cluster only fills gaps.
		// Unreachable → warn and derive from the repo alone (the honesty gate then preserves).
		if err := cluster.Augment(cmd.Context(), flagKubectl, objs); err != nil {
			fmt.Fprintf(os.Stderr, "cluster augmentation unavailable (%v);\nderiving from the repo alone — blind backends will be preserved, not resolved\n", err)
		} else {
			fmt.Fprintln(os.Stderr, "cluster augmentation: applied (blind backends resolved from live-cluster state where present)")
		}
	}
	if flagHelm {
		// Best-effort: render HelmReleases so Helm-backed backends resolve without a cluster.
		// Repo (and any cluster) objects were parsed first, so they stay authoritative.
		sum := helm.Render(cmd.Context(), flagHelmBin, objs)
		if len(sum.Rendered) > 0 {
			fmt.Fprintf(os.Stderr, "helm rendering: %d HelmRelease(s) rendered\n", len(sum.Rendered))
		}
		for _, s := range sum.Skipped {
			fmt.Fprintln(os.Stderr, "helm rendering skipped —", s)
		}
	}
	committed := func(path string) ([]byte, error) {
		return os.ReadFile(filepath.Join(flagRepo, path))
	}
	files, report, err := policy.Render(cfg, discovery.Build(objs), committed)
	if err != nil {
		return err
	}
	reportCoverage(report)

	drift := false
	for _, f := range files {
		abs := filepath.Join(flagRepo, f.Path)
		if write {
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(abs, f.Content, 0o644); err != nil {
				return err
			}
			fmt.Println("WROTE", f.Path)
		} else {
			existing, _ := os.ReadFile(abs)
			if !bytes.Equal(existing, f.Content) {
				drift = true
				fmt.Println("DRIFT", f.Path)
			}
		}
	}
	if !write && drift {
		return fmt.Errorf("policy drift detected")
	}
	return nil
}

// reportCoverage surfaces the honesty gate's finding: ingress backends PolySieve could not see
// in the repo (Helm/operator-rendered). Their ports were preserved, not pruned — the run says
// so plainly rather than silently emitting a smaller policy.
func reportCoverage(r policy.Report) {
	if len(r.BlindServices) == 0 && len(r.RouteBlindServices) == 0 {
		return
	}
	if len(r.BlindServices) > 0 {
		fmt.Fprintf(os.Stderr, "coverage: %d ingress backend(s) with no visible workload (Helm/operator pods);\n", len(r.BlindServices))
		fmt.Fprintln(os.Stderr, "their health-prober ports were preserved, not pruned:")
		for _, s := range r.BlindServices {
			fmt.Fprintln(os.Stderr, "  -", s)
		}
	}
	if len(r.RouteBlindServices) > 0 {
		fmt.Fprintf(os.Stderr, "coverage: %d ingress backend(s) whose routed port could not be resolved from the repo\n", len(r.RouteBlindServices))
		fmt.Fprintln(os.Stderr, "(Service absent or named targetPort unresolved); their route policies were preserved, not pruned:")
		for _, s := range r.RouteBlindServices {
			fmt.Fprintln(os.Stderr, "  -", s)
		}
	}
	fmt.Fprintln(os.Stderr, "resolve them with cluster/helm augmentation to enable pruning.")
}

// loadConfig returns the defaults, overlaid with the config file (--config, else
// <repo>/.polysieve.yml when present), then with the flags.
func loadConfig() (config.Config, error) {
	cfg := config.Default()
	path := flagConfig
	if path == "" {
		if p := filepath.Join(flagRepo, ".polysieve.yml"); fileExists(p) {
			path = p
		}
	}
	if path != "" {
		file, err := config.Load(path)
		if err != nil {
			return cfg, err
		}
		cfg.Overlay(file)
	}
	cfg.Overlay(over)
	off := false
	for _, d := range flagDisable {
		switch d {
		case "contract":
			cfg.Outputs.Contract.Enabled = &off
		case "healthcheck":
			cfg.Outputs.Healthcheck.Enabled = &off
		case "gateway-ingress":
			cfg.Outputs.GatewayIngress.Enabled = &off
		default:
			return cfg, fmt.Errorf("--disable %q: known outputs are contract, healthcheck, gateway-ingress", d)
		}
	}
	return cfg, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
