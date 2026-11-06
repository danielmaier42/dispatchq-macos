package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultRepo is the GitHub repository dispatchq releases are published to.
const DefaultRepo = "danielmaier42/dispatchq-app"

// GitHubFetcher retrieves the latest release from GitHub's public REST API.
type GitHubFetcher struct {
	// Repo is "owner/name"; empty uses [DefaultRepo].
	Repo string
	// Client is the HTTP client; nil uses a client with a sane timeout.
	Client *http.Client
	// BaseURL overrides the GitHub API root (for tests); empty uses the real one.
	BaseURL string
}

// ghRelease mirrors the subset of GitHub's release JSON we consume.
type ghRelease struct {
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// listPerPage caps how many recent releases we fetch. It bounds the response
// size while comfortably covering every version a user could realistically have
// skipped, so the banner can show the notes of all releases in between.
const listPerPage = 30

// Releases fetches recent published releases from GitHub, newest first, with
// drafts and pre-releases excluded. This lets the caller show the notes of
// every release a user skipped, not just the newest one.
func (g GitHubFetcher) Releases(ctx context.Context) ([]Release, error) {
	repo := g.Repo
	if repo == "" {
		repo = DefaultRepo
	}
	base := strings.TrimRight(g.BaseURL, "/")
	if base == "" {
		base = "https://api.github.com"
	}
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=%d", base, repo, listPerPage)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query GitHub releases: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// Read a little of the body for a useful message (GitHub returns JSON).
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GitHub releases API: %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}

	var grs []ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&grs); err != nil {
		return nil, fmt.Errorf("decode releases: %w", err)
	}
	out := make([]Release, 0, len(grs))
	for _, gr := range grs {
		if gr.Draft || gr.Prerelease {
			continue
		}
		out = append(out, *gr.toRelease())
	}
	return out, nil
}

// toRelease converts the GitHub payload into our Release, picking the `.pkg`
// asset as the installer download.
func (gr ghRelease) toRelease() *Release {
	rel := &Release{
		Version:     Normalize(gr.TagName),
		Tag:         gr.TagName,
		Notes:       gr.Body,
		HTMLURL:     gr.HTMLURL,
		PublishedAt: gr.PublishedAt,
	}
	for _, a := range gr.Assets {
		if strings.HasSuffix(strings.ToLower(a.Name), ".pkg") {
			rel.PkgURL = a.BrowserDownloadURL
			rel.PkgName = a.Name
			break
		}
	}
	return rel
}
