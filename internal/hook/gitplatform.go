package hook

import (
	"fmt"

	"github.com/serpro69/capy/internal/giturl"
)

// gitPlatformBlockMessage returns a block reason when capy_fetch_and_index is
// called with a git platform issue/PR/MR URL. Returns empty string for
// non-matching URLs (gists, generic pages) where the tool should proceed.
func gitPlatformBlockMessage(rawURL string) string {
	info, ok := giturl.ParsePlatformURL(rawURL)
	if !ok {
		return ""
	}

	if ghCmd := info.GhCommand(); ghCmd != "" {
		return fmt.Sprintf(
			"capy: Blocked — this is a GitHub %s. Use `%s` for full comprehension. "+
				"capy_fetch_and_index BM25-fragments the content, losing sequential context.",
			info.KindDisplay(), ghCmd,
		)
	}

	return fmt.Sprintf(
		"capy: Blocked — this URL points to a %s (#%s). "+
			"Use your platform's CLI or WebSearch for full comprehension. "+
			"capy_fetch_and_index BM25-fragments the content, losing sequential context.",
		info.KindDisplay(), info.Number,
	)
}

// gistGuidance returns soft guidance (FormatAllow) for gist URLs.
// Gists can be large, so this is advisory rather than blocking.
func gistGuidance(rawURL string) string {
	if !giturl.IsGistURL(rawURL) {
		return ""
	}
	return `<context_guidance>
  <tip>
    This URL points to a GitHub gist. For small gists needing full comprehension,
    consider using gh gist view or direct web tools first.
    capy_fetch_and_index is appropriate for large gists where you only need extracted facts.
  </tip>
</context_guidance>`
}

// webFetchBlockMessage returns the block message for WebFetch calls, with
// git-platform-specific comprehension guidance when the URL matches.
func webFetchBlockMessage(rawURL string) string {
	info, ok := giturl.ParsePlatformURL(rawURL)
	if ok {
		if ghCmd := info.GhCommand(); ghCmd != "" {
			return fmt.Sprintf(
				"capy: WebFetch blocked. This is a GitHub %s — use `%s` for full comprehension. "+
					"If you need searchable extraction of large content, use capy_fetch_and_index(url: %q) then capy_search(queries: [...]).",
				info.KindDisplay(), ghCmd, rawURL,
			)
		}

		return fmt.Sprintf(
			"capy: WebFetch blocked. This URL points to a %s (#%s). "+
				"For full comprehension, use your platform's CLI or WebSearch. "+
				"For large content extraction, use capy_fetch_and_index(url: %q) then capy_search(queries: [...]).",
			info.KindDisplay(), info.Number, rawURL,
		)
	}

	return fmt.Sprintf(
		"capy: WebFetch blocked. For comprehension of small pages, use WebSearch or direct tools (e.g., platform CLI for git issues/PRs). "+
			"For large content extraction, use capy_fetch_and_index(url: %q) then capy_search(queries: [...]).",
		rawURL,
	)
}
