package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/serpro69/capy/internal/knowledge"
	"github.com/serpro69/capy/internal/security"
	"github.com/spf13/cobra"
)

func newIndexCmd() *cobra.Command {
	var source string
	var maxDepth, maxFiles int
	var respectGitignore bool
	var options knowledge.DirectoryOptions
	cmd := &cobra.Command{
		Use:   "index <path>",
		Short: "Index a file or directory into the project's knowledge base",
		Long: `Index a file or directory as durable, file-backed knowledge.
Relative paths resolve against the selected --project-dir, not the process cwd.
The input location does not change the selected project's database or credentials.

Directory labels are canonical absolute paths, or SOURCE:relative/path when
--source is supplied. Repeating a label with unchanged content deduplicates;
removed or excluded files remain indexed until explicit cleanup.

Directory scans skip credential/build paths and nested Git roots. They visit at
most 20,000 entries and read at most 32 MiB. A limit returns exit 0 with an
incomplete summary. Operational failures or cancellation return exit 1 with
partial progress retained. Invalid arguments, credentials and storage errors
also return exit 1. Recursive filters do not apply to an explicit file.`,
		Example: `  capy index README.md
  capy index docs --source project-docs --include '**/*.md'
  capy index src --extensions go,md --exclude '**/*_test.go' --max-files 500
  capy index docs --follow-symlinks --respect-gitignore=false`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			options.MaxDepth, options.MaxFiles = &maxDepth, &maxFiles
			options.RespectGitignore = &respectGitignore
			if args[0] == "" {
				return fmt.Errorf("path must be nonempty")
			}
			if err := options.Validate(); err != nil {
				return err
			}
			return runIndex(cmd, args[0], source, options)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&source, "source", "", "file source label or directory label prefix (default: absolute file paths)")
	flags.IntVar(&maxDepth, "max-depth", 5, "directory depth, root = 0 (0–20)")
	flags.IntVar(&maxFiles, "max-files", 200, "eligible directory file attempts (1–1000)")
	flags.StringSliceVar(&options.Extensions, "extensions", nil, "directory extensions, comma-separated or repeated; empty uses md,mdx,txt,json,yaml,yml,toml,ts,tsx,js,jsx,py,rs,go,sh")
	flags.StringArrayVar(&options.Include, "include", nil, "whole root-relative include pattern using *, ? and ** (repeatable; quote globs)")
	flags.StringArrayVar(&options.Exclude, "exclude", nil, "additional root-relative exclusion pattern (repeatable; mandatory exclusions remain)")
	flags.BoolVar(&respectGitignore, "respect-gitignore", true, "apply the canonical root's Git ignores, including tracked files")
	flags.BoolVar(&options.FollowSymlinks, "follow-symlinks", false, "follow descendant links within the canonical root, retaining filters and Read policy")
	return cmd
}

func runIndex(cmd *cobra.Command, path, source string, options knowledge.DirectoryOptions) (runErr error) {
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
		// Store writes/checkpointing finish independently of the command's
		// context. Preserve their commits, but do not lose a late cancellation.
		if err := ctx.Err(); err != nil && !errors.Is(runErr, err) {
			runErr = errors.Join(runErr, err)
		}
	}()
	ingestion := knowledge.NewIndexer(target.projectDir, target.cfg.Store.MaxSourceBytes, policy, st)
	out, indexErr := ingestion.IndexPath(ctx, path, source, options)
	indexErr = errors.Join(indexErr, ctx.Err())
	if out.Directory != nil {
		return errors.Join(indexErr, writeIndexDirectory(cmd.OutOrStdout(), *out.Directory, indexErr))
	}
	if indexErr != nil {
		return indexErr
	}
	result := out.File.Result
	status, chunks := "Indexed", result.TotalChunks
	if result.AlreadyIndexed {
		status = "Unchanged"
		meta, err := st.GetSourceMeta(result.Label)
		if err != nil {
			return fmt.Errorf("read indexed source metadata: %w", err)
		}
		if meta == nil {
			return fmt.Errorf("indexed source no longer exists")
		}
		chunks = meta.ChunkCount
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %q: %d retained sections; %d bytes read\n", status, result.Label, chunks, out.File.BytesRead)
	return err
}

func writeIndexDirectory(w io.Writer, out knowledge.DirectoryResult, scanErr error) error {
	status := "complete"
	if !out.Complete {
		status = "incomplete (limit reached)"
	}
	if scanErr != nil {
		status = "partial error (committed files retained)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Directory scan: %s\nRoot: %q\n", status, out.Root)
	fmt.Fprintf(&b, "Indexed: %d; unchanged: %d; denied: %d; skipped: %d; failed: %d\n", out.Indexed, out.Unchanged, out.Denied, out.Skipped, out.Failed)
	fmt.Fprintf(&b, "Visited entries: %d; file attempts: %d; bytes read: %d; bytes submitted: %d; retained sections: %d\n", out.Visited, out.Attempts, out.BytesRead, out.BytesIndexed, out.Chunks)
	fmt.Fprintf(&b, "Git ignores: %q\n", out.GitIgnoreStatus)
	if len(out.Limits) > 0 {
		fmt.Fprintf(&b, "Limits reached: %s\n", strings.Join(out.Limits, ", "))
	}
	for _, detail := range out.Failures {
		fmt.Fprintf(&b, "Failure: %s\n", detail)
	}
	if out.OmittedFailures > 0 {
		fmt.Fprintf(&b, "%d further failures omitted.\n", out.OmittedFailures)
	}
	if out.SourcePrefix != "" {
		fmt.Fprintf(&b, "Source prefix: %q\n", out.SourcePrefix+":")
	}
	_, err := io.WriteString(w, b.String())
	return err
}
