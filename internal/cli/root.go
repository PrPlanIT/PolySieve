// Package cli wires PolySieve's cobra command tree.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	flagRepo      string
	flagProfile   string
	flagKustomize string
	flagRoots     []string
	flagVerbose   bool
)

var rootCmd = &cobra.Command{
	Use:   "polysieve",
	Short: "Derive Kubernetes network policy from a GitOps repository",
	Long: `PolySieve reads a GitOps repository's own manifests (the source of truth) and
derives the least-privilege network authorization policy each namespace needs, emitting it
in the shape a given cluster expects (selected by --profile).`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&flagRepo, "repo", ".", "path to the GitOps repository root")
	rootCmd.PersistentFlags().StringVar(&flagProfile, "profile", "dungeon", "cluster profile that shapes the emitted policy")
	rootCmd.PersistentFlags().StringVar(&flagKustomize, "kustomize", "kustomize", "kustomize binary to render with")
	rootCmd.PersistentFlags().StringSliceVar(&flagRoots, "root", nil, "override the profile's kustomize build roots (repeatable)")
	rootCmd.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false, "verbose output")
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
