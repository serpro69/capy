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
// N must be numeric, optionally with a .diff/.patch suffix, so documentation
// paths such as /en/pull-requests/<topic> are not mistaken for platform items.
func ParsePlatformURL(rawURL string) (Info, bool) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return Info{}, false
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	info := Info{Host: u.Host}

	// GitHub: owner/repo/issues/N or owner/repo/pull/N
	if u.Host == "github.com" && len(parts) >= 4 {
		if number, ok := itemNumber(parts[3]); ok {
			info.Owner = parts[0]
			info.Repo = parts[1]
			switch parts[2] {
			case "issues":
				info.Number = number
				info.Kind = "issue"
				return info, true
			case "pull":
				info.Number = number
				info.Kind = "pr"
				return info, true
			}
		}
	}

	// Generic: scan for issue/PR markers at any path position.
	// Covers GitLab (/-/issues/N, /-/merge_requests/N), Bitbucket
	// (/issues/N, /pull-requests/N), Gitea (/issues/N, /pulls/N), and others.
	for i := 0; i < len(parts)-1; i++ {
		number, ok := itemNumber(parts[i+1])
		if !ok {
			continue
		}
		switch parts[i] {
		case "issues":
			info.Number = number
			info.Kind = "issue"
			return info, true
		case "pull", "pulls", "pull-requests":
			info.Number = number
			info.Kind = "pr"
			return info, true
		case "merge_requests":
			info.Number = number
			info.Kind = "mr"
			return info, true
		}
	}

	return Info{}, false
}

// itemNumber returns the ASCII digits of an issue/PR/MR path segment. A
// trailing .diff or .patch (raw change endpoints) is accepted and dropped.
func itemNumber(segment string) (string, bool) {
	for _, suffix := range []string{".diff", ".patch"} {
		segment = strings.TrimSuffix(segment, suffix)
	}
	if segment == "" {
		return "", false
	}
	for i := 0; i < len(segment); i++ {
		if segment[i] < '0' || segment[i] > '9' {
			return "", false
		}
	}
	return segment, true
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
