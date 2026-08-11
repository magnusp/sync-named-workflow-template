// Package templatesync compares local workflow template files against the
// copies committed in target repositories and distributes any differences
// via pull requests.
package templatesync

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/magnusp/sync-named-workflow-template/internal/ghclient"
)

const targetDir = ".github/workflow-templates"

// Repo identifies a target repository as "owner/name".
type Repo struct {
	Owner string
	Name  string
}

// ParseRepo splits "owner/name" into a Repo.
func ParseRepo(s string) (Repo, error) {
	owner, name, ok := cut(s, '/')
	if !ok || owner == "" || name == "" {
		return Repo{}, fmt.Errorf("invalid repo %q: want owner/name", s)
	}
	return Repo{Owner: owner, Name: name}, nil
}

func cut(s string, sep byte) (before, after string, found bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

// TemplateFile is a local file to distribute, along with the path it should
// be written to in each target repository.
type TemplateFile struct {
	// TargetPath is the path within the target repo, e.g.
	// ".github/workflow-templates/renovate.yml".
	TargetPath string
	Content    []byte
}

// LoadTemplateFiles reads every regular file directly under dir and maps it
// to its destination path under .github/workflow-templates/ in target repos.
func LoadTemplateFiles(dir string) ([]TemplateFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading template dir: %w", err)
	}

	var files []TemplateFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading template file %s: %w", entry.Name(), err)
		}
		files = append(files, TemplateFile{
			TargetPath: targetDir + "/" + entry.Name(),
			Content:    content,
		})
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no template files found in %s", dir)
	}
	return files, nil
}

// gitBlobSHA computes the SHA-1 git uses to identify a blob's content,
// matching the "sha" field GitHub's contents API returns.
func gitBlobSHA(content []byte) string {
	h := sha1.New()
	_, _ = fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// FileDrift describes the sync state of a single file in a single repo.
type FileDrift struct {
	TargetPath string
	// RemoteSHA is empty when the file does not exist in the target repo.
	RemoteSHA string
	Changed   bool
}

// RepoPlan is the drift report for one repository.
type RepoPlan struct {
	Repo          Repo
	DefaultBranch string
	Files         []FileDrift
}

// Drifted reports whether any file in the plan differs from the template.
func (p RepoPlan) Drifted() bool {
	for _, f := range p.Files {
		if f.Changed {
			return true
		}
	}
	return false
}

// githubClient is the subset of ghclient.Client used by this package,
// declared here so tests can substitute a fake.
type githubClient interface {
	DefaultBranch(owner, repo string) (string, error)
	GetContent(owner, repo, path, ref string) (*ghclient.ContentFile, error)
	RefSHA(owner, repo, branch string) (string, error)
	CreateRef(owner, repo, branch, sha string) error
	UpdateRef(owner, repo, branch, sha string) error
	PutContent(owner, repo string, in ghclient.PutContentInput) error
	FindOpenPR(owner, repo, headBranch string) (*ghclient.PullRequest, error)
	CreatePR(owner, repo, head, base, title, body string) (*ghclient.PullRequest, error)
}

// Planner computes and applies drift plans against a GitHub API client.
type Planner struct {
	Client githubClient
	Files  []TemplateFile
}

// NewPlanner builds a Planner backed by a real ghclient.Client.
func NewPlanner(client *ghclient.Client, files []TemplateFile) *Planner {
	return &Planner{Client: client, Files: files}
}

// Plan compares the template files against what's currently committed on
// repo's default branch.
func (p *Planner) Plan(repo Repo) (RepoPlan, error) {
	branch, err := p.Client.DefaultBranch(repo.Owner, repo.Name)
	if err != nil {
		return RepoPlan{}, fmt.Errorf("resolving default branch for %s/%s: %w", repo.Owner, repo.Name, err)
	}

	plan := RepoPlan{Repo: repo, DefaultBranch: branch}
	for _, tf := range p.Files {
		wantSHA := gitBlobSHA(tf.Content)

		remote, err := p.Client.GetContent(repo.Owner, repo.Name, tf.TargetPath, branch)
		if errors.Is(err, ghclient.ErrNotFound) {
			plan.Files = append(plan.Files, FileDrift{TargetPath: tf.TargetPath, Changed: true})
			continue
		}
		if err != nil {
			return RepoPlan{}, fmt.Errorf("fetching %s from %s/%s: %w", tf.TargetPath, repo.Owner, repo.Name, err)
		}

		plan.Files = append(plan.Files, FileDrift{
			TargetPath: tf.TargetPath,
			RemoteSHA:  remote.SHA,
			Changed:    remote.SHA != wantSHA,
		})
	}
	return plan, nil
}

// BranchState describes what, if anything, already exists for a sync branch.
type BranchState struct {
	BranchExists bool
	OpenPR       *ghclient.PullRequest
}

// BranchName is the stable branch name used for every sync PR.
const BranchName = "sync/workflow-templates"

// InspectBranch reports whether the sync branch and/or an open PR for it
// already exist in repo.
func (p *Planner) InspectBranch(repo Repo) (BranchState, error) {
	var state BranchState

	_, err := p.Client.RefSHA(repo.Owner, repo.Name, BranchName)
	switch {
	case errors.Is(err, ghclient.ErrNotFound):
		// No existing branch; nothing more to check.
		return state, nil
	case err != nil:
		return state, fmt.Errorf("checking branch %s on %s/%s: %w", BranchName, repo.Owner, repo.Name, err)
	}
	state.BranchExists = true

	pr, err := p.Client.FindOpenPR(repo.Owner, repo.Name, BranchName)
	if err != nil {
		return state, fmt.Errorf("checking open PRs on %s/%s: %w", repo.Owner, repo.Name, err)
	}
	state.OpenPR = pr
	return state, nil
}

// Apply pushes the drifted files in plan onto the sync branch (creating or
// force-updating it as needed) and opens a PR if one isn't already open.
func (p *Planner) Apply(plan RepoPlan) (*ghclient.PullRequest, error) {
	repo := plan.Repo

	baseSHA, err := p.Client.RefSHA(repo.Owner, repo.Name, plan.DefaultBranch)
	if err != nil {
		return nil, fmt.Errorf("resolving %s HEAD on %s/%s: %w", plan.DefaultBranch, repo.Owner, repo.Name, err)
	}

	if _, err := p.Client.RefSHA(repo.Owner, repo.Name, BranchName); errors.Is(err, ghclient.ErrNotFound) {
		if err := p.Client.CreateRef(repo.Owner, repo.Name, BranchName, baseSHA); err != nil {
			return nil, fmt.Errorf("creating branch %s on %s/%s: %w", BranchName, repo.Owner, repo.Name, err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("checking branch %s on %s/%s: %w", BranchName, repo.Owner, repo.Name, err)
	} else {
		if err := p.Client.UpdateRef(repo.Owner, repo.Name, BranchName, baseSHA); err != nil {
			return nil, fmt.Errorf("resetting branch %s on %s/%s: %w", BranchName, repo.Owner, repo.Name, err)
		}
	}

	byPath := make(map[string]TemplateFile, len(p.Files))
	for _, tf := range p.Files {
		byPath[tf.TargetPath] = tf
	}

	for _, fd := range plan.Files {
		if !fd.Changed {
			continue
		}
		tf, ok := byPath[fd.TargetPath]
		if !ok {
			return nil, fmt.Errorf("internal error: no template content for %s", fd.TargetPath)
		}

		// The branch was just reset to the default branch's HEAD, so any
		// prior SHA on the sync branch no longer applies; re-check what's
		// there now (if anything) to satisfy the contents API's update
		// requirement.
		var sha string
		if existing, err := p.Client.GetContent(repo.Owner, repo.Name, fd.TargetPath, BranchName); err == nil {
			sha = existing.SHA
		} else if !errors.Is(err, ghclient.ErrNotFound) {
			return nil, fmt.Errorf("fetching %s on %s: %w", fd.TargetPath, BranchName, err)
		}

		err = p.Client.PutContent(repo.Owner, repo.Name, ghclient.PutContentInput{
			Path:    fd.TargetPath,
			Branch:  BranchName,
			Message: "sync: update " + fd.TargetPath,
			Content: tf.Content,
			SHA:     sha,
		})
		if err != nil {
			return nil, fmt.Errorf("writing %s on %s/%s: %w", fd.TargetPath, repo.Owner, repo.Name, err)
		}
	}

	pr, err := p.Client.FindOpenPR(repo.Owner, repo.Name, BranchName)
	if err != nil {
		return nil, fmt.Errorf("checking open PRs on %s/%s: %w", repo.Owner, repo.Name, err)
	}
	if pr != nil {
		return pr, nil
	}

	pr, err = p.Client.CreatePR(repo.Owner, repo.Name, BranchName, plan.DefaultBranch,
		"sync: update workflow templates",
		"Automated by sync-named-workflow-template to update workflow templates from the source template directory.")
	if err != nil {
		return nil, fmt.Errorf("opening PR on %s/%s: %w", repo.Owner, repo.Name, err)
	}
	return pr, nil
}
