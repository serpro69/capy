package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/serpro69/capy/internal/sanitize"
	"github.com/serpro69/capy/internal/security"
	"github.com/serpro69/capy/internal/store"
	"github.com/spf13/cobra"
)

const searchSnippetBytes = 1500

func newSearchCmd() *cobra.Command {
	opts := store.SearchOptions{FailOnError: true}
	var limit int
	var kinds []string
	cmd := &cobra.Command{
		Use:   "search <query...>",
		Short: "Search the selected project's knowledge base",
		Long: `Search knowledge in the selected project's encrypted database.
Query arguments are joined with spaces. Results include a source, title and a
snippet of at most 1,500 bytes; long snippets show a prefix with an ellipsis.

Only durable knowledge is included by default. Repeat --include-kind to select
durable and/or ephemeral content. A nonempty --source is a literal substring
selector (%, _ and backslash are literal) and bypasses kind filtering.
--type selects code or prose chunks. There is no MCP search throttle.

This command searches knowledge only. Use capy vault search for sessions.
Bounded stale-file checks use the selected project's Read policy and persist
progress across invocations; cached content remains searchable if a refresh is
denied or deferred. A search does not validate every backing file.

Results and no matches both return exit 0. Invalid arguments, credential, policy,
storage, output or close failures and cancellation return exit 1.`,
		Example: `  capy search connection pooling
  capy search timeout --source 'project-docs:' --limit 10
  capy search handler --type code
  capy search build --include-kind durable --include-kind ephemeral
  capy search migrations --project-dir /path/to/project`,
		Args:         cobra.MinimumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			query := strings.TrimSpace(strings.Join(args, " "))
			if query == "" {
				return fmt.Errorf("query must be nonempty")
			}
			if limit < 1 || limit > 100 {
				return fmt.Errorf("limit must be between 1 and 100")
			}
			if cmd.Flags().Changed("type") && opts.ContentType != "code" && opts.ContentType != "prose" {
				return fmt.Errorf("type must be code or prose")
			}
			opts.IncludeKinds = nil
			for _, kind := range kinds {
				if kind != string(store.KindDurable) && kind != string(store.KindEphemeral) {
					return fmt.Errorf("include-kind must be durable or ephemeral; use capy vault search for sessions")
				}
				opts.IncludeKinds = append(opts.IncludeKinds, store.SourceKind(kind))
			}
			return runSearch(cmd, query, limit, opts)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&opts.Source, "source", "", "literal source-label substring; bypasses kind filtering")
	flags.IntVar(&limit, "limit", 3, "maximum results (1–100)")
	flags.StringVar(&opts.ContentType, "type", "", "chunk type: code or prose (default: both)")
	flags.StringArrayVar(&kinds, "include-kind", []string{"durable"}, "knowledge kind: durable or ephemeral (repeatable; replaces the default)")
	return cmd
}

func runSearch(cmd *cobra.Command, query string, limit int, opts store.SearchOptions) (runErr error) {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := loadKnowledgeTarget(cmd)
	if err != nil {
		return err
	}
	policy, err := security.LoadReadPolicy(security.FilePolicyContext{ProjectDir: target.projectDir})
	if err != nil {
		return fmt.Errorf("preparing Read policy: %w", err)
	}
	for _, warning := range policy.Warnings() {
		if _, err := fmt.Fprintln(cmd.ErrOrStderr(), warning); err != nil {
			return fmt.Errorf("write policy warning: %w", err)
		}
	}
	key, keySource, err := resolveKnowledgeKey(target)
	if err != nil {
		return err
	}
	st := newKnowledgeStore(target, key, keySource.String())
	defer func() {
		if err := st.Close(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("close knowledge store: %w", err))
		}
		// Access tracking and checkpointing can finish after cancellation.
		if err := ctx.Err(); err != nil && !errors.Is(runErr, err) {
			runErr = errors.Join(runErr, err)
		}
	}()
	st.SetDenyChecker(func(path string) bool {
		if err := policy.Check(path); err != nil {
			slog.Warn("stale refresh: Read policy blocked file", "error", err)
			return true
		}
		return false
	})
	results, err := st.SearchWithFallbackContext(ctx, query, limit, opts)
	if err != nil {
		return fmt.Errorf("search knowledge: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeKnowledgeSearch(cmd.OutOrStdout(), results)
}

func writeKnowledgeSearch(w io.Writer, results []store.SearchResult) error {
	if len(results) == 0 {
		_, err := fmt.Fprintln(w, "No knowledge results found.")
		return err
	}
	for i, result := range results {
		_, err := fmt.Fprintf(w, "%d. Source: %q\nTitle: %q\n%s\n\n", i+1,
			knowledgeSearchText(result.Label, 512), knowledgeSearchText(result.Title, 256),
			knowledgeSearchText(result.Content, searchSnippetBytes))
		if err != nil {
			return fmt.Errorf("write search results: %w", err)
		}
	}
	return nil
}

// Sanitize before truncating so a cutoff cannot hide the end of a credential
// from the redactor. Bound metadata as well as content, preserving valid UTF-8
// and removing terminal controls from untrusted stored text.
func knowledgeSearchText(s string, maxBytes int) string {
	s = sanitize.StripSecrets(s)
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return '�'
		}
		return r
	}, s)
	if len(s) <= maxBytes {
		return s
	}
	end := maxBytes - len("…")
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "…"
}
