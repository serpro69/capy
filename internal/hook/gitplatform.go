package hook

import (
	"fmt"
	"net/url"
	"strings"
)

// gitURLInfo holds parsed information about a git platform issue/PR URL.
type gitURLInfo struct {
	Host   string // e.g. "github.com", "gitlab.com", "gitea.example.com"
	Owner  string // populated for GitHub; may be empty for other platforms
	Repo   string // populated for GitHub; may be empty for other platforms
	Number string // issue/PR/MR number
	Kind   string // "issue", "pr", or "mr"
}

// parseGitPlatformURL detects if a URL points to a git platform issue, PR, or
// merge request. Recognizes GitHub, GitLab, Bitbucket, Gitea, and generic
// platforms using common path conventions (/issues/N, /pull/N, /merge_requests/N).
func parseGitPlatformURL(rawURL string) (info gitURLInfo, ok bool) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return gitURLInfo{}, false
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	info.Host = u.Host

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

	return gitURLInfo{}, false
}

// isGistURL returns true for gist.github.com and gist.githubusercontent.com URLs.
func isGistURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return u.Host == "gist.github.com" || u.Host == "gist.githubusercontent.com"
}

// fetchGuidance returns comprehension guidance when capy_fetch_and_index is
// called with a git platform issue/PR/MR or gist URL. Returns empty string
// for URLs where capy_fetch_and_index is the correct tool.
func fetchGuidance(rawURL string) string {
	info, ok := parseGitPlatformURL(rawURL)
	if ok {
		// GitHub-specific: suggest exact gh command
		if info.Host == "github.com" && info.Owner != "" {
			switch info.Kind {
			case "issue":
				return fmt.Sprintf(`<context_guidance>
  <tip>
    This URL points to a GitHub issue. For full comprehension, use:
      gh issue view %s --repo %s/%s
    capy_fetch_and_index will BM25-fragment the content, losing sequential context.
    Proceeding with fetch — but the gh command gives you the complete issue with comments.
  </tip>
</context_guidance>`, info.Number, info.Owner, info.Repo)
			case "pr":
				return fmt.Sprintf(`<context_guidance>
  <tip>
    This URL points to a GitHub PR. For full comprehension, use:
      gh pr view %s --repo %s/%s
    capy_fetch_and_index will BM25-fragment the content, losing sequential context.
    Proceeding with fetch — but the gh command gives you the complete PR with review comments.
  </tip>
</context_guidance>`, info.Number, info.Owner, info.Repo)
			}
		}

		// Generic git platform: suggest alternatives without platform-specific CLI
		kindName := info.Kind
		if kindName == "mr" {
			kindName = "merge request"
		}
		return fmt.Sprintf(`<context_guidance>
  <tip>
    This URL points to a git platform %s (#%s). For full comprehension, use your
    platform's CLI or WebSearch — these are small authoritative pages.
    capy_fetch_and_index will BM25-fragment the content, losing sequential context.
    Proceeding with fetch — but a direct tool gives you the complete content.
  </tip>
</context_guidance>`, kindName, info.Number)
	}

	if isGistURL(rawURL) {
		return `<context_guidance>
  <tip>
    This URL points to a GitHub gist. For small gists needing full comprehension,
    consider using gh gist view or direct web tools first.
    capy_fetch_and_index is appropriate for large gists where you only need extracted facts.
  </tip>
</context_guidance>`
	}

	return ""
}

// webFetchBlockMessage returns the block message for WebFetch calls, with
// git-platform-specific comprehension guidance when the URL matches.
func webFetchBlockMessage(rawURL string) string {
	info, ok := parseGitPlatformURL(rawURL)
	if ok {
		// GitHub-specific: suggest exact gh command
		if info.Host == "github.com" && info.Owner != "" {
			var ghCmd string
			switch info.Kind {
			case "issue":
				ghCmd = fmt.Sprintf("gh issue view %s --repo %s/%s", info.Number, info.Owner, info.Repo)
			case "pr":
				ghCmd = fmt.Sprintf("gh pr view %s --repo %s/%s", info.Number, info.Owner, info.Repo)
			}
			return fmt.Sprintf(
				"capy: WebFetch blocked. This is a GitHub %s — use `%s` for full comprehension. "+
					"If you need searchable extraction of large content, use capy_fetch_and_index(url: %q) then capy_search(queries: [...]).",
				info.Kind, ghCmd, rawURL,
			)
		}

		// Generic git platform
		kindName := info.Kind
		if kindName == "mr" {
			kindName = "merge request"
		}
		return fmt.Sprintf(
			"capy: WebFetch blocked. This URL points to a %s (#%s). "+
				"For full comprehension, use your platform's CLI or WebSearch. "+
				"For large content extraction, use capy_fetch_and_index(url: %q) then capy_search(queries: [...]).",
			kindName, info.Number, rawURL,
		)
	}

	return fmt.Sprintf(
		"capy: WebFetch blocked. For comprehension of small pages, use WebSearch or direct tools (e.g., platform CLI for git issues/PRs). "+
			"For large content extraction, use capy_fetch_and_index(url: %q) then capy_search(queries: [...]).",
		rawURL,
	)
}
