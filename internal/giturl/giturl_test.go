package giturl

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParsePlatformURL(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		wantHost   string
		wantOwner  string
		wantRepo   string
		wantNumber string
		wantKind   string
		wantOK     bool
	}{
		{"GitHub issue", "https://github.com/serpro69/capy/issues/44", "github.com", "serpro69", "capy", "44", "issue", true},
		{"GitHub PR", "https://github.com/serpro69/capy/pull/47", "github.com", "serpro69", "capy", "47", "pr", true},
		{"GitHub issue with fragment", "https://github.com/serpro69/capy/issues/44#issuecomment-123", "github.com", "serpro69", "capy", "44", "issue", true},
		{"GitHub PR with subpath", "https://github.com/serpro69/capy/pull/47/files", "github.com", "serpro69", "capy", "47", "pr", true},
		{"GitLab issue", "https://gitlab.com/group/project/-/issues/123", "gitlab.com", "", "", "123", "issue", true},
		{"GitLab MR", "https://gitlab.com/group/project/-/merge_requests/456", "gitlab.com", "", "", "456", "mr", true},
		{"Bitbucket PR", "https://bitbucket.org/team/repo/pull-requests/78", "bitbucket.org", "", "", "78", "pr", true},
		{"Gitea issue", "https://gitea.example.com/org/repo/issues/99", "gitea.example.com", "", "", "99", "issue", true},
		{"Gitea pull", "https://gitea.example.com/org/repo/pulls/12", "gitea.example.com", "", "", "12", "pr", true},
		{"repo root", "https://github.com/serpro69/capy", "", "", "", "", "", false},
		{"issues listing", "https://github.com/serpro69/capy/issues", "", "", "", "", "", false},
		{"gist", "https://gist.github.com/serpro69/abc123", "", "", "", "", "", false},
		{"non-git URL", "https://docs.example.com/api/reference", "", "", "", "", "", false},
		{"empty", "", "", "", "", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, ok := ParsePlatformURL(tt.url)
			assert.Equal(t, tt.wantOK, ok)
			if ok {
				assert.Equal(t, tt.wantHost, info.Host)
				assert.Equal(t, tt.wantOwner, info.Owner)
				assert.Equal(t, tt.wantRepo, info.Repo)
				assert.Equal(t, tt.wantNumber, info.Number)
				assert.Equal(t, tt.wantKind, info.Kind)
			}
		})
	}
}

func TestIsGistURL(t *testing.T) {
	assert.True(t, IsGistURL("https://gist.github.com/serpro69/abc123"))
	assert.True(t, IsGistURL("https://gist.githubusercontent.com/serpro69/abc123/raw/file.txt"))
	assert.False(t, IsGistURL("https://github.com/serpro69/capy/issues/44"))
	assert.False(t, IsGistURL("https://docs.example.com"))
	assert.False(t, IsGistURL(""))
}

func TestGhCommand(t *testing.T) {
	t.Run("GitHub issue", func(t *testing.T) {
		info := Info{Host: "github.com", Owner: "serpro69", Repo: "capy", Number: "44", Kind: "issue"}
		assert.Equal(t, "gh issue view 44 --repo serpro69/capy", info.GhCommand())
	})

	t.Run("GitHub PR", func(t *testing.T) {
		info := Info{Host: "github.com", Owner: "serpro69", Repo: "capy", Number: "47", Kind: "pr"}
		assert.Equal(t, "gh pr view 47 --repo serpro69/capy", info.GhCommand())
	})

	t.Run("non-GitHub returns empty", func(t *testing.T) {
		info := Info{Host: "gitlab.com", Number: "123", Kind: "issue"}
		assert.Empty(t, info.GhCommand())
	})
}

func TestKindDisplay(t *testing.T) {
	assert.Equal(t, "issue", Info{Kind: "issue"}.KindDisplay())
	assert.Equal(t, "pr", Info{Kind: "pr"}.KindDisplay())
	assert.Equal(t, "merge request", Info{Kind: "mr"}.KindDisplay())
}
