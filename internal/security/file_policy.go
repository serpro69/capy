package security

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// FilePolicyContext separates settings ownership from cwd-relative matching.
// MCP/CLI use the selected project for both directories. Hooks can supply a
// validated working directory without changing the selected project's settings.
type FilePolicyContext struct {
	ProjectDir         string
	WorkingDir         string
	HomeDir            string
	GlobalSettingsPath string
}

// FilePolicy is an immutable, origin-aware Read policy snapshot. It checks
// explicit file inputs, not arbitrary filesystem access by submitted code.
type FilePolicy struct {
	context  FilePolicyContext
	rules    []preparedReadRule
	warnings []string
}

type preparedReadRule struct {
	pattern, action, source, anchor string
	all                             bool
	matchers                        []*regexp.Regexp
	legacy                          *regexp.Regexp // single-slash denies only
}

// Warnings returns each compatibility diagnostic once per rule/source.
func (p *FilePolicy) Warnings() []string {
	return slices.Clone(p.warnings)
}

// Check denies a read if either its lexical spelling or physical target is
// denied. Missing targets are left to the caller's normal file-error handling.
// Other resolution errors fail closed. This check is not atomic with a later
// open; see upstream-sync-v1.0.169/upstream-audit.md, deferred issue D2.
func (p *FilePolicy) Check(path string) error {
	_, err := p.checkedCandidates(path)
	return err
}

// Allows reports whether one explicit allow covers both the requested path and
// its physical target. Denies still win. Ask rules are retained/validated but
// do not grant access: MCP has no host permission UI.
func (p *FilePolicy) Allows(path string) (bool, error) {
	candidates, err := p.checkedCandidates(path)
	if err != nil {
		return false, err
	}
	// A grant requires an existing, resolvable physical target.
	if len(candidates) < 2 {
		return false, nil
	}
	return p.allowsCandidates(candidates), nil
}

func (p *FilePolicy) allowsCandidates(candidates []string) bool {
	for _, rule := range p.rules {
		if rule.action != "allow" {
			continue
		}
		matches := true
		for _, candidate := range candidates {
			if !rule.matches(candidate) {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}

// ResolveExecuteFile admits a path relative to the selected project and returns
// its checked physical absolute path for the executor. Both lexical and physical
// containment are required unless one explicit Read allow covers both paths.
// This protects only the path parameter, not filesystem access by submitted code.
// TODO(D2): bind admission to the runtime read with an atomic checked-file handoff;
// returning a path cannot prevent concurrent replacement before the child reads.
// See docs/feat/wip/upstream-sync-v1.0.169/upstream-audit.md#d2-filesystem-replacement-between-policy-checks-and-runtime-reads.
func (p *FilePolicy) ResolveExecuteFile(path string) (string, error) {
	if p == nil {
		return "", fmt.Errorf("Read policy is unavailable")
	}
	project, err := filepath.EvalSymlinks(p.context.ProjectDir)
	if err != nil {
		return "", fmt.Errorf("resolve execute-file project: %w", err)
	}
	info, err := os.Stat(project)
	if err != nil {
		return "", fmt.Errorf("stat execute-file project: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("execute-file project %q is not a directory", project)
	}
	candidates, err := p.resolveCandidates(path, p.context.ProjectDir, true)
	if err != nil {
		return "", err
	}
	lexical, physical := candidates[0], candidates[1]
	local := (pathWithin(p.context.ProjectDir, lexical) || pathWithin(project, lexical)) && pathWithin(project, physical)
	if !local && !p.allowsCandidates(candidates) {
		return "", fmt.Errorf("execute-file path %q is outside the selected project without an explicit Read allow covering the requested and physical paths; add a matching permissions.allow rule in .claude/settings.local.json (for example Read(//absolute/path/**))", path)
	}
	return physical, nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (p *FilePolicy) checkedCandidates(path string) ([]string, error) {
	if p == nil {
		return nil, fmt.Errorf("Read policy is unavailable")
	}
	return p.resolveCandidates(path, p.context.WorkingDir, false)
}

func (p *FilePolicy) resolveCandidates(path, base string, requireExisting bool) ([]string, error) {
	if p == nil {
		return nil, fmt.Errorf("Read policy is unavailable")
	}
	if path == "" || strings.ContainsRune(path, 0) {
		return nil, fmt.Errorf("invalid file path")
	}
	if err := p.checkDenies(path); err != nil {
		return nil, err
	}
	// Do not use Join here: it cleans away symlinks before a following '..'.
	input := path
	if !filepath.IsAbs(input) {
		input = base + string(filepath.Separator) + input
	}
	candidates := []string{filepath.Clean(input)}
	if err := p.checkDenies(input); err != nil {
		return nil, err
	}
	if err := p.checkDenies(candidates[0]); err != nil {
		return nil, err
	}
	physical, err := filepath.EvalSymlinks(input)
	if err != nil {
		if os.IsNotExist(err) && !requireExisting {
			return candidates, nil
		}
		return nil, fmt.Errorf("resolve file path %q: %w", path, err)
	}
	if err := p.checkDenies(physical); err != nil {
		return nil, err
	}
	return append(candidates, physical), nil
}

func (p *FilePolicy) checkDenies(path string) error {
	for _, rule := range p.rules {
		if rule.action == "deny" && rule.matches(path) {
			return fmt.Errorf("path matches Read deny pattern %q from %q", rule.pattern, rule.source)
		}
	}
	return nil
}

func (r preparedReadRule) matches(path string) bool {
	if r.all {
		return true
	}
	// Preserve even the old embedded-**/ and backslash-normalization semantics
	// for legacy absolute denies. This compatibility arm can only restrict.
	if r.legacy != nil && r.legacy.MatchString(strings.ReplaceAll(path, "\\", "/")) {
		return true
	}
	for _, matcher := range r.matchers {
		if matcher.MatchString(filepath.ToSlash(path)) {
			return true
		}
	}
	return false
}

func normalizeFilePolicyContext(ctx FilePolicyContext) (FilePolicyContext, error) {
	for _, dir := range []*string{&ctx.ProjectDir, &ctx.WorkingDir} {
		if *dir == "" {
			*dir = ctx.ProjectDir
		}
		abs, err := filepath.Abs(*dir)
		if err != nil {
			return ctx, fmt.Errorf("resolve policy directory: %w", err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return ctx, fmt.Errorf("resolve policy directory %q: %w", abs, err)
		}
		if !info.IsDir() {
			return ctx, fmt.Errorf("policy directory %q is not a directory", abs)
		}
		*dir = abs
	}
	if ctx.HomeDir == "" {
		var err error
		ctx.HomeDir, err = os.UserHomeDir()
		if err != nil {
			return ctx, fmt.Errorf("resolve policy home: %w", err)
		}
	}
	if !filepath.IsAbs(ctx.HomeDir) {
		return ctx, fmt.Errorf("policy home must be absolute")
	}
	if ctx.GlobalSettingsPath == "" {
		ctx.GlobalSettingsPath = filepath.Join(ctx.HomeDir, ".claude", "settings.json")
	}
	var err error
	ctx.GlobalSettingsPath, err = filepath.Abs(ctx.GlobalSettingsPath)
	return ctx, err
}

func prepareReadRule(pattern, action, source, sourceAnchor string, ctx FilePolicyContext) (preparedReadRule, string, error) {
	rule := preparedReadRule{pattern: pattern, action: action, source: source}
	if pattern == "Read" {
		rule.all = true
		return rule, "", nil
	}
	if !strings.HasSuffix(pattern, ")") {
		return rule, "", fmt.Errorf("expected Read(path) or bare Read")
	}
	glob := strings.TrimSuffix(strings.TrimPrefix(pattern, "Read("), ")")
	anchor, body := ctx.WorkingDir, glob
	basename := !strings.Contains(glob, "/")
	legacy := false
	// Recognize anchors before cleaning: Clean would erase the // distinction.
	switch {
	case strings.HasPrefix(glob, "//"):
		anchor, body = string(filepath.Separator), glob[2:]
	case strings.HasPrefix(glob, "~/"):
		anchor, body = ctx.HomeDir, glob[2:]
	case strings.HasPrefix(glob, "/"):
		anchor, body, legacy = sourceAnchor, glob[1:], action == "deny"
	case strings.HasPrefix(glob, "./"):
		body = glob[2:]
	case strings.HasPrefix(glob, "~"):
		return rule, "", fmt.Errorf("unsupported home syntax; use ~/")
	}
	rule.anchor = anchor
	matchers, err := prepareFileMatchers(anchor, body, basename, action == "deny")
	if err != nil {
		return rule, "", err
	}
	rule.matchers = matchers
	var warning string
	if legacy {
		rule.legacy = fileGlobToRegex(glob, false)
		matchers, err := prepareFileMatchers(string(filepath.Separator), body, false, true)
		if err != nil {
			return rule, "", err
		}
		rule.matchers = append(rule.matchers, matchers...)
		warning = fmt.Sprintf("ambiguous Read deny %q in %q: preserving settings-relative and legacy absolute matches; use Read(/%s) for an unambiguous filesystem-root rule", pattern, source, glob)
	}
	return rule, warning, nil
}

// prepareFileMatchers resolves the literal prefix of denies too, so a deny
// expressed through a symlink also protects a caller using its physical path.
// Allows resolve only the anchor: resolving their literal prefix would turn an
// allowed-looking alias into a grant for an otherwise ungranted external target.
func prepareFileMatchers(anchor, glob string, basename, deny bool) ([]*regexp.Regexp, error) {
	expression, literal, err := readGlobExpression(glob)
	if err != nil {
		return nil, err
	}
	prefix := anchor
	if !basename {
		prefix += "/" + literal
	}
	if basename {
		if expression == "" {
			expression = regexp.QuoteMeta(literal)
		}
		expression = `(?:.*/)?` + expression
	}
	paths := []string{filepath.Clean(prefix)}
	physicalInput := prefix
	if !deny {
		physicalInput = anchor
	}
	physical, err := resolvePolicyPrefix(physicalInput)
	if err != nil {
		return nil, err
	}
	if !deny && !basename {
		physical += "/" + literal
	}
	paths = append(paths, filepath.Clean(physical))
	var matchers []*regexp.Regexp
	for _, path := range paths {
		head := regexp.QuoteMeta(filepath.ToSlash(path))
		if basename || expression != "" {
			if !strings.HasSuffix(path, string(filepath.Separator)) {
				head += "/"
			}
		}
		matcher, err := regexp.Compile(`(?s)^` + head + expression + `$`)
		if err != nil {
			return nil, fmt.Errorf("compile Read path pattern: %w", err)
		}
		matchers = append(matchers, matcher)
	}
	return matchers, nil
}

// Resolve existing ancestors while allowing rules for paths not created yet.
// Dir/Join would clean '..' before symlink resolution, so peel raw components.
func resolvePolicyPrefix(path string) (string, error) {
	physical, err := filepath.EvalSymlinks(path)
	if err == nil {
		return physical, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve Read policy anchor %q: %w", path, err)
	}
	index := strings.LastIndex(strings.TrimRight(path, "/"), "/")
	if index < 0 {
		return "", fmt.Errorf("Read policy anchor is not absolute")
	}
	parent := path[:index]
	if parent == "" {
		parent = "/"
	}
	physical, err = resolvePolicyPrefix(parent)
	if err != nil {
		return "", err
	}
	return physical + "/" + path[index+1:], nil
}

// readGlobExpression returns a regex suffix plus the decoded literal directory
// prefix. Only *, **, ? and backslash-escaped literals are supported. Reject
// syntax we cannot honor instead of quietly weakening a rule.
func readGlobExpression(glob string) (expression, literal string, err error) {
	if glob == "" || !utf8.ValidString(glob) || strings.ContainsRune(glob, 0) || strings.HasPrefix(glob, "!") {
		return "", "", fmt.Errorf("empty, invalid or unsupported Read path pattern")
	}
	var parts []string
	var prefix []string
	wild := false
	for _, part := range strings.Split(glob, "/") {
		var re, decoded strings.Builder
		partWild := false
		for i := 0; i < len(part); i++ {
			switch part[i] {
			case '\\':
				i++
				if i == len(part) {
					return "", "", fmt.Errorf("dangling Read pattern escape")
				}
				re.WriteString(regexp.QuoteMeta(part[i : i+1]))
				decoded.WriteByte(part[i])
			case '*':
				partWild = true
				if i+1 < len(part) && part[i+1] == '*' {
					re.WriteString(".*")
					i++
				} else {
					re.WriteString("[^/]*")
				}
			case '?':
				partWild = true
				re.WriteString("[^/]")
			case '[', ']', '{', '}', '(', ')':
				return "", "", fmt.Errorf("unsupported Read pattern syntax; escape literal metacharacters")
			default:
				re.WriteString(regexp.QuoteMeta(part[i : i+1]))
				decoded.WriteByte(part[i])
			}
		}
		if !wild && !partWild {
			prefix = append(prefix, decoded.String())
			continue
		}
		wild = true
		if part == ".." || part == "." {
			return "", "", fmt.Errorf("dot components after wildcards are unsupported")
		}
		if part == "**" {
			parts = append(parts, "**")
		} else {
			parts = append(parts, re.String())
		}
	}
	var result strings.Builder
	for i, part := range parts {
		if part == "**" && i < len(parts)-1 {
			result.WriteString(`(?:.*/)?`)
			continue
		}
		if part == "**" {
			result.WriteString(".*")
		} else {
			result.WriteString(part)
		}
		if i < len(parts)-1 {
			result.WriteByte('/')
		}
	}
	return result.String(), strings.Join(prefix, "/"), nil
}
