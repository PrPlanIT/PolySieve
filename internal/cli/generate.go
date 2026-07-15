package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/PrPlanIT/PolySieve/internal/cluster"
	"github.com/PrPlanIT/PolySieve/internal/discovery"
	"github.com/PrPlanIT/PolySieve/internal/profile"
	"github.com/PrPlanIT/PolySieve/internal/profile/dungeon"
)

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
	prof, err := selectProfile(flagProfile)
	if err != nil {
		return err
	}
	roots := flagRoots
	if len(roots) == 0 {
		roots = prof.Roots()
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
	committed := func(path string) ([]byte, error) {
		return os.ReadFile(filepath.Join(flagRepo, path))
	}
	files, report, err := prof.Render(discovery.Build(objs), committed)
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
func reportCoverage(r profile.Report) {
	if len(r.BlindServices) == 0 && len(r.RouteBlindServices) == 0 {
		return
	}
	if len(r.BlindServices) > 0 {
		fmt.Fprintf(os.Stderr, "coverage: %d ingress backend(s) with no visible workload (Helm/operator pods);\n", len(r.BlindServices))
		fmt.Fprintln(os.Stderr, "their Gatus healthcheck ports were preserved, not pruned:")
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

func selectProfile(name string) (profile.Profile, error) {
	switch name {
	case "dungeon":
		return dungeon.New(), nil
	default:
		return nil, fmt.Errorf("unknown profile %q (known: dungeon)", name)
	}
}
