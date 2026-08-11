package templatesync

import (
	"crypto/sha1"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/magnusp/sync-named-workflow-template/internal/ghclient"
)

func blobSHA(content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func TestParseRepo(t *testing.T) {
	repo, err := ParseRepo("acme/widgets")
	if err != nil {
		t.Fatalf("ParseRepo: %v", err)
	}
	if repo.Owner != "acme" || repo.Name != "widgets" {
		t.Errorf("repo = %+v", repo)
	}

	if _, err := ParseRepo("invalid"); err == nil {
		t.Error("expected error for repo without owner/name")
	}
}

func TestLoadTemplateFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "renovate.yml"), []byte("a: 1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "renovate.properties.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	files, err := LoadTemplateFiles(dir)
	if err != nil {
		t.Fatalf("LoadTemplateFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2", len(files))
	}
	paths := map[string]bool{}
	for _, f := range files {
		paths[f.TargetPath] = true
	}
	if !paths[".github/workflow-templates/renovate.yml"] || !paths[".github/workflow-templates/renovate.properties.json"] {
		t.Errorf("unexpected paths: %+v", paths)
	}
}

func TestLoadTemplateFilesEmpty(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadTemplateFiles(dir); err == nil {
		t.Error("expected error for empty template dir")
	}
}

// fakeClient is an in-memory stand-in for the GitHub API used to exercise
// Planner without network access.
type fakeClient struct {
	defaultBranch string
	// files maps branch -> path -> (content, sha)
	files map[string]map[string]fakeFile
	refs  map[string]string // branch -> commit sha
	prs   map[string]*ghclient.PullRequest
}

type fakeFile struct {
	content []byte
	sha     string
}

func newFakeClient(defaultBranch string) *fakeClient {
	return &fakeClient{
		defaultBranch: defaultBranch,
		files:         map[string]map[string]fakeFile{defaultBranch: {}},
		refs:          map[string]string{defaultBranch: "base-sha"},
		prs:           map[string]*ghclient.PullRequest{},
	}
}

func (f *fakeClient) seed(branch, path string, content []byte) {
	if f.files[branch] == nil {
		f.files[branch] = map[string]fakeFile{}
	}
	f.files[branch][path] = fakeFile{content: content, sha: blobSHA(content)}
}

func (f *fakeClient) DefaultBranch(owner, repo string) (string, error) {
	return f.defaultBranch, nil
}

func (f *fakeClient) GetContent(owner, repo, path, ref string) (*ghclient.ContentFile, error) {
	branchFiles, ok := f.files[ref]
	if !ok {
		return nil, ghclient.ErrNotFound
	}
	file, ok := branchFiles[path]
	if !ok {
		return nil, ghclient.ErrNotFound
	}
	return &ghclient.ContentFile{SHA: file.sha, Path: path}, nil
}

func (f *fakeClient) RefSHA(owner, repo, branch string) (string, error) {
	sha, ok := f.refs[branch]
	if !ok {
		return "", ghclient.ErrNotFound
	}
	return sha, nil
}

func (f *fakeClient) CreateRef(owner, repo, branch, sha string) error {
	if _, exists := f.refs[branch]; exists {
		return fmt.Errorf("branch %s already exists", branch)
	}
	f.refs[branch] = sha
	f.files[branch] = map[string]fakeFile{}
	return nil
}

func (f *fakeClient) UpdateRef(owner, repo, branch, sha string) error {
	f.refs[branch] = sha
	f.files[branch] = map[string]fakeFile{}
	return nil
}

func (f *fakeClient) PutContent(owner, repo string, in ghclient.PutContentInput) error {
	if f.files[in.Branch] == nil {
		f.files[in.Branch] = map[string]fakeFile{}
	}
	f.files[in.Branch][in.Path] = fakeFile{content: in.Content, sha: blobSHA(in.Content)}
	return nil
}

func (f *fakeClient) FindOpenPR(owner, repo, headBranch string) (*ghclient.PullRequest, error) {
	return f.prs[headBranch], nil
}

func (f *fakeClient) CreatePR(owner, repo, head, base, title, body string) (*ghclient.PullRequest, error) {
	pr := &ghclient.PullRequest{Number: len(f.prs) + 1, HTMLURL: "https://example.com/pr/" + head, State: "open"}
	f.prs[head] = pr
	return pr, nil
}

var testFiles = []TemplateFile{
	{TargetPath: ".github/workflow-templates/renovate.yml", Content: []byte("a: 1")},
	{TargetPath: ".github/workflow-templates/renovate.properties.json", Content: []byte("{}")},
}

func TestPlanNoDrift(t *testing.T) {
	client := newFakeClient("main")
	for _, tf := range testFiles {
		client.seed("main", tf.TargetPath, tf.Content)
	}
	planner := &Planner{Client: client, Files: testFiles}

	plan, err := planner.Plan(Repo{Owner: "acme", Name: "widgets"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Drifted() {
		t.Errorf("plan should not be drifted: %+v", plan)
	}
}

func TestPlanDriftMissingAndChanged(t *testing.T) {
	client := newFakeClient("main")
	client.seed("main", testFiles[0].TargetPath, []byte("a: 2")) // different content
	// second file missing entirely
	planner := &Planner{Client: client, Files: testFiles}

	plan, err := planner.Plan(Repo{Owner: "acme", Name: "widgets"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plan.Drifted() {
		t.Fatal("expected drift")
	}
	for _, f := range plan.Files {
		if !f.Changed {
			t.Errorf("expected %s to be changed", f.TargetPath)
		}
	}
}

func TestInspectBranchNoBranch(t *testing.T) {
	client := newFakeClient("main")
	planner := &Planner{Client: client, Files: testFiles}

	state, err := planner.InspectBranch(Repo{Owner: "acme", Name: "widgets"})
	if err != nil {
		t.Fatalf("InspectBranch: %v", err)
	}
	if state.BranchExists {
		t.Error("expected branch not to exist")
	}
}

func TestInspectBranchExistingWithPR(t *testing.T) {
	client := newFakeClient("main")
	client.refs[BranchName] = "branch-sha"
	client.prs[BranchName] = &ghclient.PullRequest{Number: 3, HTMLURL: "https://example.com/pr/3"}
	planner := &Planner{Client: client, Files: testFiles}

	state, err := planner.InspectBranch(Repo{Owner: "acme", Name: "widgets"})
	if err != nil {
		t.Fatalf("InspectBranch: %v", err)
	}
	if !state.BranchExists {
		t.Error("expected branch to exist")
	}
	if state.OpenPR == nil || state.OpenPR.Number != 3 {
		t.Errorf("state.OpenPR = %+v", state.OpenPR)
	}
}

func TestApplyCreatesBranchAndPR(t *testing.T) {
	client := newFakeClient("main")
	client.seed("main", testFiles[0].TargetPath, []byte("a: 2"))
	planner := &Planner{Client: client, Files: testFiles}
	repo := Repo{Owner: "acme", Name: "widgets"}

	plan, err := planner.Plan(repo)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	pr, err := planner.Apply(plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if pr == nil {
		t.Fatal("expected a PR")
	}

	for _, tf := range testFiles {
		got := client.files[BranchName][tf.TargetPath]
		if string(got.content) != string(tf.Content) {
			t.Errorf("branch content for %s = %q, want %q", tf.TargetPath, got.content, tf.Content)
		}
	}
}

func TestApplyReusesExistingOpenPR(t *testing.T) {
	client := newFakeClient("main")
	client.seed("main", testFiles[0].TargetPath, []byte("a: 2"))
	client.refs[BranchName] = "old-branch-sha"
	existingPR := &ghclient.PullRequest{Number: 42, HTMLURL: "https://example.com/pr/42"}
	client.prs[BranchName] = existingPR

	planner := &Planner{Client: client, Files: testFiles}
	repo := Repo{Owner: "acme", Name: "widgets"}

	plan, err := planner.Plan(repo)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	pr, err := planner.Apply(plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if pr.Number != 42 {
		t.Errorf("pr.Number = %d, want 42 (reused)", pr.Number)
	}
}
