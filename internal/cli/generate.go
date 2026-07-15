package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

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
	files, err := prof.Render(discovery.Build(objs))
	if err != nil {
		return err
	}

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

func selectProfile(name string) (profile.Profile, error) {
	switch name {
	case "dungeon":
		return dungeon.New(), nil
	default:
		return nil, fmt.Errorf("unknown profile %q (known: dungeon)", name)
	}
}
