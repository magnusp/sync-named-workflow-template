package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/magnusp/sync-named-workflow-template/internal/ghclient"
	"github.com/magnusp/sync-named-workflow-template/internal/templatesync"
	"github.com/spf13/cobra"
)

var (
	syncTemplateDir string
	syncRepos       []string
	syncAssumeYes   bool
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync workflow template files to a list of GitHub repositories",
	Long: "Compares local workflow template files against the copies committed in each\n" +
		"target repository and opens a pull request in any repository where they differ.",
	RunE: runSync,
}

func init() {
	syncCmd.Flags().StringVar(&syncTemplateDir, "template-dir", "", "directory containing the template files to distribute (required)")
	syncCmd.Flags().StringSliceVar(&syncRepos, "repo", nil, "target repository as owner/name (repeatable, or comma-separated)")
	syncCmd.Flags().BoolVarP(&syncAssumeYes, "yes", "y", false, "proceed without interactive confirmation")
	_ = syncCmd.MarkFlagRequired("template-dir")
	_ = syncCmd.MarkFlagRequired("repo")

	rootCmd.AddCommand(syncCmd)
}

func runSync(cmd *cobra.Command, args []string) error {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("GITHUB_TOKEN is not set; run `export GITHUB_TOKEN=$(gh auth token)`")
	}

	repos := make([]templatesync.Repo, 0, len(syncRepos))
	for _, r := range syncRepos {
		repo, err := templatesync.ParseRepo(r)
		if err != nil {
			return err
		}
		repos = append(repos, repo)
	}

	files, err := templatesync.LoadTemplateFiles(syncTemplateDir)
	if err != nil {
		return err
	}

	planner := templatesync.NewPlanner(ghclient.New(token), files)
	out := cmd.OutOrStdout()
	in := bufio.NewReader(cmd.InOrStdin())

	for _, repo := range repos {
		plan, err := planner.Plan(repo)
		if err != nil {
			return err
		}

		if !plan.Drifted() {
			fmt.Fprintf(out, "%s/%s: up to date\n", repo.Owner, repo.Name)
			continue
		}

		fmt.Fprintf(out, "%s/%s: drift detected\n", repo.Owner, repo.Name)
		for _, f := range plan.Files {
			if f.Changed {
				fmt.Fprintf(out, "  changed: %s\n", f.TargetPath)
			}
		}

		state, err := planner.InspectBranch(repo)
		if err != nil {
			return err
		}
		if state.BranchExists && !syncAssumeYes {
			msg := fmt.Sprintf("%s already has a %q branch", fullName(repo), templatesync.BranchName)
			if state.OpenPR != nil {
				msg += fmt.Sprintf(" with an open PR (%s)", state.OpenPR.HTMLURL)
			}
			if !confirm(out, in, msg+". Overwrite it and continue?") {
				fmt.Fprintf(out, "%s: skipped\n", fullName(repo))
				continue
			}
		}

		pr, err := planner.Apply(plan)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: %s\n", fullName(repo), pr.HTMLURL)
	}

	return nil
}

func fullName(repo templatesync.Repo) string {
	return repo.Owner + "/" + repo.Name
}

func confirm(out io.Writer, in *bufio.Reader, prompt string) bool {
	fmt.Fprintf(out, "%s [y/N] ", prompt)
	line, _ := in.ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
