package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/j4n-e4t/rollout/internal/scaffold"
)

func newInitCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Add example files to the environment repository",
	}
	var force bool
	renovate := &cobra.Command{
		Use:   "renovate",
		Short: "Write an example Renovate config (renovate.json5) next to rollout.json",
		Long: `Write an example Renovate config next to rollout.json.

It pins images to digests, opens one PR per service, groups non-major updates
and holds major ones for approval. Enable the Renovate GitHub app on the
repository to start receiving PRs.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := filepath.Dir(g.configPath)
			target := filepath.Join(dir, "renovate.json5")
			if !force {
				for _, name := range []string{"renovate.json5", "renovate.json", ".renovaterc", ".renovaterc.json", ".github/renovate.json", ".github/renovate.json5"} {
					p := filepath.Join(dir, name)
					if _, err := os.Stat(p); err == nil {
						return fmt.Errorf("%s already exists (use --force to write %s anyway)", p, target)
					} else if !errors.Is(err, fs.ErrNotExist) {
						return err
					}
				}
			}
			if err := os.WriteFile(target, scaffold.Renovate, 0o644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", target)
			return nil
		},
	}
	renovate.Flags().BoolVar(&force, "force", false, "overwrite an existing renovate.json5")
	cmd.AddCommand(renovate)
	return cmd
}
