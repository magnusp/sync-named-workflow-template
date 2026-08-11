package ghclient

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, Token: "test-token", HTTP: srv.Client()}
}

func TestGetContent(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer test-token"; got != want {
			t.Errorf("Authorization header = %q, want %q", got, want)
		}
		if got, want := r.URL.Path, "/repos/acme/widgets/contents/foo.yml"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		json.NewEncoder(w).Encode(ContentFile{
			SHA:     "abc123",
			Content: base64.StdEncoding.EncodeToString([]byte("hello")),
			Path:    "foo.yml",
		})
	})

	got, err := c.GetContent("acme", "widgets", "foo.yml", "main")
	if err != nil {
		t.Fatalf("GetContent: %v", err)
	}
	if got.SHA != "abc123" {
		t.Errorf("SHA = %q, want abc123", got.SHA)
	}
	content, err := got.DecodedContent()
	if err != nil {
		t.Fatalf("DecodedContent: %v", err)
	}
	if string(content) != "hello" {
		t.Errorf("content = %q, want hello", content)
	}
}

func TestGetContentNotFound(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	_, err := c.GetContent("acme", "widgets", "missing.yml", "main")
	if err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestRefSHA(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/repos/acme/widgets/git/ref/heads/main"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"object": map[string]string{"sha": "deadbeef"},
		})
	})

	sha, err := c.RefSHA("acme", "widgets", "main")
	if err != nil {
		t.Fatalf("RefSHA: %v", err)
	}
	if sha != "deadbeef" {
		t.Errorf("sha = %q, want deadbeef", sha)
	}
}

func TestCreateRef(t *testing.T) {
	var gotBody map[string]string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
	})

	if err := c.CreateRef("acme", "widgets", "sync/x", "deadbeef"); err != nil {
		t.Fatalf("CreateRef: %v", err)
	}
	if gotBody["ref"] != "refs/heads/sync/x" || gotBody["sha"] != "deadbeef" {
		t.Errorf("body = %+v", gotBody)
	}
}

func TestUpdateRef(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s, want PATCH", r.Method)
		}
		if got, want := r.URL.Path, "/repos/acme/widgets/git/refs/heads/sync/x"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
	})

	if err := c.UpdateRef("acme", "widgets", "sync/x", "deadbeef"); err != nil {
		t.Fatalf("UpdateRef: %v", err)
	}
}

func TestPutContent(t *testing.T) {
	var gotBody map[string]any
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
	})

	err := c.PutContent("acme", "widgets", PutContentInput{
		Path:    "foo.yml",
		Branch:  "sync/x",
		Message: "sync: update foo.yml",
		Content: []byte("hello"),
		SHA:     "abc123",
	})
	if err != nil {
		t.Fatalf("PutContent: %v", err)
	}
	if gotBody["sha"] != "abc123" || gotBody["branch"] != "sync/x" {
		t.Errorf("body = %+v", gotBody)
	}
	wantContent := base64.StdEncoding.EncodeToString([]byte("hello"))
	if gotBody["content"] != wantContent {
		t.Errorf("content = %v, want %v", gotBody["content"], wantContent)
	}
}

func TestFindOpenPR(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Query().Get("head"), "acme:sync/x"; got != want {
			t.Errorf("head query = %q, want %q", got, want)
		}
		json.NewEncoder(w).Encode([]PullRequest{{Number: 7, HTMLURL: "https://example.com/pr/7", State: "open"}})
	})

	pr, err := c.FindOpenPR("acme", "widgets", "sync/x")
	if err != nil {
		t.Fatalf("FindOpenPR: %v", err)
	}
	if pr == nil || pr.Number != 7 {
		t.Fatalf("pr = %+v", pr)
	}
}

func TestFindOpenPRNone(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]PullRequest{})
	})

	pr, err := c.FindOpenPR("acme", "widgets", "sync/x")
	if err != nil {
		t.Fatalf("FindOpenPR: %v", err)
	}
	if pr != nil {
		t.Fatalf("pr = %+v, want nil", pr)
	}
}

func TestCreatePR(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		json.NewEncoder(w).Encode(PullRequest{Number: 9, HTMLURL: "https://example.com/pr/9"})
	})

	pr, err := c.CreatePR("acme", "widgets", "sync/x", "main", "title", "body")
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if pr.Number != 9 {
		t.Errorf("pr.Number = %d, want 9", pr.Number)
	}
}
