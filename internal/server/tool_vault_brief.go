package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/vault"
)

// vaultBriefToolName is the MCP name of the session brief tool.
const vaultBriefToolName = "capy_vault_brief"

// vaultBriefErrorMaxBytes bounds a storage or compatibility diagnostic.
const vaultBriefErrorMaxBytes = 1024

// vaultBriefArgs are the only arguments capy_vault_brief accepts.
var vaultBriefArgs = []string{"session", "project", "all_projects"}

// vaultBriefAction is the fixed instruction an eligible main-transcript search
// hit carries. It names no selector value: the caller repeats its own search
// arguments, so a long or sensitive project selector is never interpolated.
const vaultBriefAction = "Latest exchange: call capy_vault_brief with this session ID and the same project/all_projects arguments."

func toolVaultBrief() mcp.Tool {
	return mcp.NewTool(vaultBriefToolName,
		mcp.WithToolAnnotation(annotationReadOnly),
		mcp.WithDescription("Show one archived session's opening request and latest indexed exchange. Returns the session's ID, stored platform, current project and title, its opening indexed user entry, its latest indexed user entry and the last indexed assistant entry in that turn, each cited by its zero-based message-start line in the main transcript, in at most 4,096 bytes. \"Latest\" is the latest exchange in the vault's index, which can lag a live session; there is no historical cutoff, so through_line and other line arguments are rejected. Pass a full session ID or a literal prefix of at least 8 characters; an ambiguous prefix asks for a longer ID. Scoped like capy_vault_search (current project by default): after a search hit, pass its full session ID with the same project/all_projects arguments. Requires CAPY_VAULT_KEY. Reads one committed snapshot without creating, migrating or repairing the vault; use `capy vault show` for the full transcript."),
		mcp.WithString("session",
			mcp.Required(),
			mcp.Description("Full session ID or literal prefix of at least 8 characters. Every character matches literally, including % and _."),
		),
		mcp.WithString("project",
			mcp.Description("Match this literal substring against the effective project (custom label when set, otherwise imported path). Omitted or empty scopes by the current directory in the imported path, even after reassignment. Exact \"*\" is reserved for all projects; all_projects: true overrides any project selector."),
		),
		mcp.WithBoolean("all_projects",
			mcp.Description("Look the session up across every archived project instead of just the current one (default: false)."),
		),
		mcp.WithSchemaAdditionalProperties(false),
	)
}

// handleVaultBrief serves capy_vault_brief: one archived session's opening
// request and latest indexed exchange, rendered by the same vault renderer as
// `capy vault brief`. It reads through the vault's own read-only connection
// (VaultStore.ReadBrief), never the server's lazily opened writer.
//
// Validation errors return before vault access and stay untracked, matching
// the other vault handlers; every later outcome is tracked once.
func (s *Server) handleVaultBrief(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	session, widened, err := validateVaultBriefArgs(req.GetArguments())
	if err != nil {
		return errorResult("Error: " + err.Error()), nil
	}
	project, projectPath, err := vaultProjectScope(req, s.projectDir)
	if err != nil {
		return errorResult("Error: " + err.Error()), nil
	}

	vlt := s.getVault()
	if vlt == nil {
		return s.trackToolResponse(vaultBriefToolName, errorResult(
			"Error: session briefs are disabled — set CAPY_VAULT_KEY to archive and read past sessions.")), nil
	}
	// An empty project path would otherwise mean the whole vault, silently
	// widening a request that asked for the current project.
	if !widened && project == "" && projectPath == "" {
		return s.trackToolResponse(vaultBriefToolName, errorResult(
			"Error: the default scope is the current project, but this server has no project directory; pass a project selector or all_projects: true.")), nil
	}

	b, err := vlt.ReadBrief(ctx, session, vault.BriefScope{Project: project, ProjectPath: projectPath})
	if err != nil {
		return s.trackToolResponse(vaultBriefToolName, errorResult(vaultBriefError(session, widened, err))), nil
	}
	return s.trackToolResponse(vaultBriefToolName, textResult(vault.RenderBrief(b))), nil
}

// validateVaultBriefArgs checks argument names and types before vault access.
// It returns the session selector and whether the request widens to every
// project.
func validateVaultBriefArgs(args map[string]any) (session string, widened bool, err error) {
	var unknown []string
	for name := range args {
		if !slices.Contains(vaultBriefArgs, name) {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		// A historical cutoff is a request this tool cannot honor; answering it
		// with the latest exchange would give the caller the wrong answer.
		if slices.Contains(unknown, "through_line") {
			return "", false, errors.New("through_line is not supported: the brief shows only the latest indexed exchange; use `capy vault show` for earlier messages")
		}
		slices.Sort(unknown)
		return "", false, fmt.Errorf("unsupported parameter %s: capy_vault_brief accepts only session, project and all_projects", argPreview(unknown[0]))
	}

	raw, ok := args["session"]
	if !ok {
		return "", false, errors.New("provide session (a session ID or a literal prefix of at least 8 characters)")
	}
	session, ok = raw.(string)
	if !ok {
		return "", false, errors.New(`invalid parameter "session": expected a string`)
	}
	if err := vault.ValidateBriefSession(session); err != nil {
		return "", false, err
	}

	var project string
	if raw, ok := args["project"]; ok {
		if project, ok = raw.(string); !ok {
			return "", false, errors.New(`invalid parameter "project": expected a string`)
		}
		if strings.IndexByte(project, 0) >= 0 {
			return "", false, errors.New("project selector must not contain NUL")
		}
	}
	allProjects, err := boolArg(args, "all_projects", false)
	if err != nil {
		return "", false, err
	}
	return session, allProjects || project == "*", nil
}

// vaultBriefError names the input problem, ambiguity or scoped miss, or passes
// through the vault's own diagnostic. It never lists candidates or a count:
// the lookup stops at the second scoped match.
func vaultBriefError(session string, widened bool, err error) string {
	switch {
	case errors.Is(err, vault.ErrBriefAmbiguous):
		return fmt.Sprintf("Error: session id %s matches more than one session in the requested scope — use a longer ID.", argPreview(session))
	case errors.Is(err, vault.ErrSessionNotFound):
		msg := fmt.Sprintf("Error: no session in the requested scope matches %s.", argPreview(session))
		if !widened {
			msg += " A session archived under another project needs a matching project selector or all_projects: true."
		}
		return msg
	}
	return "Error: " + displayField(err.Error(), vaultBriefErrorMaxBytes)
}
