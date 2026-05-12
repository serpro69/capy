package giturl

import (
	"net/url"
	"strings"
)

// Info holds parsed information about a git platform issue/PR URL.
type Info struct {
	Host   string // e.g. "github.com", "gitlab.com", "gitea.example.com"
	Owner  string // populated for GitHub; may be empty for other platforms
	Repo   string // populated for GitHub; may be empty for other platforms
	Number string // issue/PR/MR number
	Kind   string // "issue", "pr", or "mr"
}

// ParsePlatformURL detects if a URL points to a git platform issue, PR, or
// merge request. Recognizes GitHub, GitLab, Bitbucket, Gitea, and generic
// platforms using common path conventions (/issues/N, /pull/N, /merge_requests/N).
func ParsePlatformURL(rawURL string) (Info, bool) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return Info{}, false
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	info := Info{Host: u.Host}

	// GitHub: owner/repo/issues/N or owner/repo/pull/N
	if u.Host == "github.com" && len(parts) >= 4 {
		info.Owner = parts[0]
		info.Repo = parts[1]
		switch parts[2] {
		case "issues":
			info.Number = parts[3]
			info.Kind = "issue"
			return info, true
		case "pull":
			info.Number = parts[3]
			info.Kind = "pr"
			return info, true
		}
	}

	// Generic: scan for issue/PR markers at any path position.
	// Covers GitLab (/-/issues/N, /-/merge_requests/N), Bitbucket
	// (/issues/N, /pull-requests/N), Gitea (/issues/N, /pulls/N), and others.
	for i := 0; i < len(parts)-1; i++ {
		switch parts[i] {
		case "issues":
			info.Number = parts[i+1]
			info.Kind = "issue"
			return info, true
		case "pull", "pulls", "pull-requests":
			info.Number = parts[i+1]
			info.Kind = "pr"
			return info, true
		case "merge_requests":
			info.Number = parts[i+1]
			info.Kind = "mr"
			return info, true
		}
	}

	return Info{}, false
}

// IsGistURL returns true for gist.github.com and gist.githubusercontent.com URLs.
func IsGistURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return u.Host == "gist.github.com" || u.Host == "gist.githubusercontent.com"
}

// KindDisplay returns a human-readable name for the Kind field.
func (i Info) KindDisplay() string {
	if i.Kind == "mr" {
		return "merge request"
	}
	return i.Kind
}

// GhCommand returns the gh CLI command for GitHub URLs, or empty string for other platforms.
func (i Info) GhCommand() string {
	if i.Host != "github.com" || i.Owner == "" {
		return ""
	}
	switch i.Kind {
	case "issue":
		return "gh issue view " + i.Number + " --repo " + i.Owner + "/" + i.Repo
	case "pr":
		return "gh pr view " + i.Number + " --repo " + i.Owner + "/" + i.Repo
	}
	return ""
}
