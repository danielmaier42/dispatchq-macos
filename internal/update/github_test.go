package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const sampleReleasesJSON = `[
  {
    "tag_name": "v0.1.4",
    "html_url": "https://github.com/danielmaier42/dispatchq-app/releases/tag/v0.1.4",
    "body": "Fixed a bug.\nAdded a thing.",
    "published_at": "2026-07-20T09:31:27Z",
    "assets": [
      {"name": "checksums.txt", "browser_download_url": "https://example.test/checksums.txt"},
      {"name": "dispatchq-0.1.4.pkg", "browser_download_url": "https://example.test/dispatchq-0.1.4.pkg"}
    ]
  },
  {
    "tag_name": "v0.1.4-rc1",
    "prerelease": true,
    "body": "release candidate",
    "assets": []
  },
  {
    "tag_name": "v0.1.3",
    "draft": true,
    "body": "unpublished draft",
    "assets": []
  },
  {
    "tag_name": "v0.1.2",
    "body": "Earlier release.",
    "assets": [{"name": "dispatchq-0.1.2.pkg", "browser_download_url": "https://example.test/dispatchq-0.1.2.pkg"}]
  }
]`

func TestGitHubFetcherReleases(t *testing.T) {
	var gotPath, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAccept = r.Header.Get("Accept")
		_, _ = w.Write([]byte(sampleReleasesJSON))
	}))
	defer srv.Close()

	f := GitHubFetcher{BaseURL: srv.URL}
	rels, err := f.Releases(context.Background())
	if err != nil {
		t.Fatalf("Releases: %v", err)
	}
	if gotPath != "/repos/danielmaier42/dispatchq-app/releases" {
		t.Errorf("request path = %q", gotPath)
	}
	if gotAccept != "application/vnd.github+json" {
		t.Errorf("Accept header = %q", gotAccept)
	}
	// Draft and pre-release entries are filtered out; 0.1.4 and 0.1.2 remain.
	if len(rels) != 2 {
		t.Fatalf("expected 2 stable releases, got %d: %+v", len(rels), rels)
	}
	if rels[0].Version != "0.1.4" || rels[1].Version != "0.1.2" {
		t.Errorf("versions = %q, %q", rels[0].Version, rels[1].Version)
	}
	if rels[0].PkgURL != "https://example.test/dispatchq-0.1.4.pkg" || rels[0].PkgName != "dispatchq-0.1.4.pkg" {
		t.Errorf("pkg selection wrong: %q / %q", rels[0].PkgURL, rels[0].PkgName)
	}
	if rels[0].Notes == "" || rels[0].HTMLURL == "" || rels[0].PublishedAt.IsZero() {
		t.Errorf("notes/html/published not populated: %+v", rels[0])
	}
}

func TestGitHubFetcherNoPkgAsset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v0.2.0","assets":[{"name":"notes.txt","browser_download_url":"x"}]}]`))
	}))
	defer srv.Close()

	rels, err := GitHubFetcher{BaseURL: srv.URL}.Releases(context.Background())
	if err != nil {
		t.Fatalf("Releases: %v", err)
	}
	if len(rels) != 1 || rels[0].PkgURL != "" {
		t.Errorf("pkg url should be empty without a .pkg asset, got %+v", rels)
	}
}

func TestGitHubFetcherErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := (GitHubFetcher{BaseURL: srv.URL}).Releases(context.Background()); err == nil {
		t.Fatal("expected an error for a 404 response")
	}
}
