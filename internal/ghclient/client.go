// Package ghclient is a minimal GitHub REST API client covering only the
// operations sync-named-workflow-template needs: reading file contents,
// resolving refs, writing files, and managing pull requests.
package ghclient

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const defaultBaseURL = "https://api.github.com"

// ErrNotFound is returned by lookups when GitHub responds 404.
var ErrNotFound = fmt.Errorf("not found")

// Client is a small wrapper around the GitHub REST API.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New builds a Client authenticated with the given token.
func New(token string) *Client {
	return &Client{
		BaseURL: defaultBaseURL,
		Token:   token,
		HTTP:    http.DefaultClient,
	}
}

func (c *Client) do(method, path string, body any, out any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, c.BaseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return resp, ErrNotFound
	}
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return resp, fmt.Errorf("github api %s %s: %s: %s", method, path, resp.Status, string(data))
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp, err
		}
	}
	return resp, nil
}

// ContentFile is the subset of the GitHub "contents" API response used here.
type ContentFile struct {
	SHA     string `json:"sha"`
	Content string `json:"content"`
	Path    string `json:"path"`
}

// DecodedContent returns the file content decoded from base64.
func (f *ContentFile) DecodedContent() ([]byte, error) {
	return base64.StdEncoding.DecodeString(f.Content)
}

// GetContent fetches a file's contents at the given ref. It returns
// ErrNotFound if the file does not exist.
func (c *Client) GetContent(owner, repo, path, ref string) (*ContentFile, error) {
	var out ContentFile
	url := fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s", owner, repo, path, ref)
	if _, err := c.do(http.MethodGet, url, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type repoInfo struct {
	DefaultBranch string `json:"default_branch"`
}

// DefaultBranch returns the repository's default branch name.
func (c *Client) DefaultBranch(owner, repo string) (string, error) {
	var out repoInfo
	url := fmt.Sprintf("/repos/%s/%s", owner, repo)
	if _, err := c.do(http.MethodGet, url, nil, &out); err != nil {
		return "", err
	}
	return out.DefaultBranch, nil
}

type gitRef struct {
	Object struct {
		SHA string `json:"sha"`
	} `json:"object"`
}

// RefSHA returns the commit SHA a branch currently points to. It returns
// ErrNotFound if the branch does not exist.
func (c *Client) RefSHA(owner, repo, branch string) (string, error) {
	var out gitRef
	url := fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s", owner, repo, branch)
	if _, err := c.do(http.MethodGet, url, nil, &out); err != nil {
		return "", err
	}
	return out.Object.SHA, nil
}

// CreateRef creates a new branch pointing at sha.
func (c *Client) CreateRef(owner, repo, branch, sha string) error {
	url := fmt.Sprintf("/repos/%s/%s/git/refs", owner, repo)
	body := map[string]string{
		"ref": "refs/heads/" + branch,
		"sha": sha,
	}
	_, err := c.do(http.MethodPost, url, body, nil)
	return err
}

// UpdateRef force-moves an existing branch to sha.
func (c *Client) UpdateRef(owner, repo, branch, sha string) error {
	url := fmt.Sprintf("/repos/%s/%s/git/refs/heads/%s", owner, repo, branch)
	body := map[string]any{
		"sha":   sha,
		"force": true,
	}
	_, err := c.do(http.MethodPatch, url, body, nil)
	return err
}

// PutContentInput describes a file create/update request.
type PutContentInput struct {
	Path    string
	Branch  string
	Message string
	Content []byte
	// SHA is the existing file's blob SHA. Leave empty when creating a new file.
	SHA string
}

// PutContent creates or updates a file on a branch.
func (c *Client) PutContent(owner, repo string, in PutContentInput) error {
	url := fmt.Sprintf("/repos/%s/%s/contents/%s", owner, repo, in.Path)
	body := map[string]any{
		"message": in.Message,
		"content": base64.StdEncoding.EncodeToString(in.Content),
		"branch":  in.Branch,
	}
	if in.SHA != "" {
		body["sha"] = in.SHA
	}
	_, err := c.do(http.MethodPut, url, body, nil)
	return err
}

// PullRequest is the subset of the GitHub pull request API used here.
type PullRequest struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
}

// FindOpenPR returns the open pull request for the given head branch, if any.
// It returns nil, nil when no open pull request exists.
func (c *Client) FindOpenPR(owner, repo, headBranch string) (*PullRequest, error) {
	var out []PullRequest
	url := fmt.Sprintf("/repos/%s/%s/pulls?state=open&head=%s:%s", owner, repo, owner, headBranch)
	if _, err := c.do(http.MethodGet, url, nil, &out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &out[0], nil
}

// CreatePR opens a pull request from head into base.
func (c *Client) CreatePR(owner, repo, head, base, title, body string) (*PullRequest, error) {
	var out PullRequest
	url := fmt.Sprintf("/repos/%s/%s/pulls", owner, repo)
	in := map[string]string{
		"title": title,
		"head":  head,
		"base":  base,
		"body":  body,
	}
	if _, err := c.do(http.MethodPost, url, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
