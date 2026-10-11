package knowledge

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// DirectoryOptions only affects recursive ingestion. Nil scalar pointers use
// defaults, while an explicit zero depth selects files directly in the root.
type DirectoryOptions struct {
	MaxDepth         *int
	MaxFiles         *int
	Extensions       []string
	Include          []string
	Exclude          []string
	RespectGitignore *bool
}

type directorySelection struct {
	maxDepth, maxFiles int
	respectGitignore   bool
	extensions         map[string]bool
	include, exclude   []*regexp.Regexp
}

// Validate checks every control before any filesystem or indexing work.
func (o DirectoryOptions) Validate() error {
	_, err := o.prepare()
	return err
}

func (o DirectoryOptions) prepare() (directorySelection, error) {
	s := directorySelection{maxDepth: 5, maxFiles: 200, respectGitignore: true}
	if o.MaxDepth != nil {
		s.maxDepth = *o.MaxDepth
	}
	if o.MaxFiles != nil {
		s.maxFiles = *o.MaxFiles
	}
	if o.RespectGitignore != nil {
		s.respectGitignore = *o.RespectGitignore
	}
	if s.maxDepth < 0 || s.maxDepth > 20 {
		return s, fmt.Errorf("max_depth must be an integer in 0–20")
	}
	if s.maxFiles < 1 || s.maxFiles > 1000 {
		return s, fmt.Errorf("max_files must be an integer in 1–1000")
	}
	extensions := o.Extensions
	if len(extensions) == 0 {
		extensions = strings.Fields(".md .mdx .txt .json .yaml .yml .toml .ts .tsx .js .jsx .py .rs .go .sh")
	}
	s.extensions = make(map[string]bool, len(extensions))
	for _, ext := range extensions {
		ext = strings.ToLower(strings.TrimSpace(ext))
		ext = "." + strings.TrimPrefix(ext, ".")
		if ext == "." || strings.ContainsAny(ext, "/\\*?[]{}!\x00\r\n") {
			return s, fmt.Errorf("extensions must contain literal filename extensions")
		}
		s.extensions[ext] = true
	}
	var err error
	s.include, err = compilePatterns(o.Include)
	if err != nil {
		return s, fmt.Errorf("invalid include: %w", err)
	}
	s.exclude, err = compilePatterns(o.Exclude)
	if err != nil {
		return s, fmt.Errorf("invalid exclude: %w", err)
	}
	return s, nil
}

// Patterns match whole root-relative POSIX paths. ** spans components; **/
// also matches zero components. No shell expansion or approximate syntax.
func compilePatterns(patterns []string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		if pattern == "" || strings.HasPrefix(pattern, "/") || strings.ContainsAny(pattern, "\\[]{}!\x00") ||
			(len(pattern) >= 2 && pattern[1] == ':') {
			return nil, fmt.Errorf("patterns must be relative paths using only *, ? and ** wildcards")
		}
		for _, component := range strings.Split(pattern, "/") {
			if component == ".." || component == "." || component == "" {
				return nil, fmt.Errorf("patterns cannot contain empty, . or .. components")
			}
		}
		var b strings.Builder
		b.WriteString("^")
		runes := []rune(pattern)
		for n := 0; n < len(runes); n++ {
			switch runes[n] {
			case '*':
				if n+1 < len(runes) && runes[n+1] == '*' {
					n++
					if n+1 < len(runes) && runes[n+1] == '/' {
						n++
						b.WriteString("(?:.*/)?")
					} else {
						b.WriteString(".*")
					}
				} else {
					b.WriteString("[^/]*")
				}
			case '?':
				b.WriteString("[^/]")
			default:
				b.WriteString(regexp.QuoteMeta(string(runes[n])))
			}
		}
		b.WriteString("$")
		r, err := regexp.Compile("(?s)" + b.String())
		if err != nil {
			return nil, fmt.Errorf("compile selection pattern: %w", err)
		}
		out = append(out, r)
	}
	return out, nil
}

func matchesAny(patterns []*regexp.Regexp, rel string) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(rel) {
			return true
		}
	}
	return false
}

func (s directorySelection) selectsFile(rel string) bool {
	return s.extensions[strings.ToLower(path.Ext(rel))] &&
		(len(s.include) == 0 || matchesAny(s.include, rel))
}

func mandatoryExcluded(rel string) bool {
	for _, component := range strings.Split(rel, "/") {
		switch component {
		case ".git", ".hg", ".svn", ".capy", ".aws", ".ssh", "node_modules", "vendor", "dist", "build", ".next", "coverage", ".venv", "venv", "__pycache__", "target", ".cache",
			".env", "id_rsa", "id_ed25519", "credentials":
			return true
		}
		if strings.HasPrefix(component, ".env.") || strings.HasSuffix(component, ".pem") || strings.HasSuffix(component, ".key") {
			return true
		}
	}
	return false
}
