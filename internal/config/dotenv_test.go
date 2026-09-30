package config

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDotenvKeyLiteral(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, input, want string }{
		{"unquoted", "CAPY_DB_KEY=literal-123_:/+==", "literal-123_:/+=="},
		{"whitespace_export_crlf", " \texport\t CAPY_DB_KEY \t= literal \t# comment\r\n", "literal"},
		{"single_quotes", "CAPY_DB_KEY='  $VAR `command` \\n \\" + "\"  '", "  $VAR `command` \\n \\" + "\"  "},
		{"double_quotes", `CAPY_DB_KEY=" spaces \\ and \"quotes\" " # comment`, ` spaces \ and "quotes" `},
		{"literal_hash", "CAPY_DB_KEY=literal#suffix", "literal#suffix"},
		{"leading_hash", "CAPY_DB_KEY=#literal", "#literal"},
		{"quoted_hash", `CAPY_DB_KEY="literal#suffix"#comment`, "literal#suffix"},
		{"literal_unicode", "CAPY_DB_KEY='ø🐾'", "ø🐾"},
		{"literal_non_utf8", "CAPY_DB_KEY='\xff'", "\xff"},
		{"space_only", "CAPY_DB_KEY=' \t '", " \t "},
		{"other_assignments", "OTHER=$UNEXPANDED\nEMPTY=\nexport OTHER=\"$VAR \\n $(never-run)\"\nCAPY_DB_KEY=literal\nCAPY_VAULT_KEY='untouched'\n", "literal"},
		{"comments_blank_lines", "# ignored\n\t\nCAPY_DB_KEY=literal\n# tail\n", "literal"},
		{"long_single_line", "CAPY_DB_KEY=" + strings.Repeat("k", 70000), strings.Repeat("k", 70000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, found, err := parseDotenvKey([]byte(tc.input), "/fixture/.env")
			require.NoError(t, err)
			assert.True(t, found)
			assert.True(t, key == tc.want, "literal bytes differ")
		})
	}
}

func TestParseDotenvKeyNoDeclaration(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, input string }{
		{"empty", ""},
		{"comment", "# CAPY_DB_KEY=ignored\n \t# export CAPY_DB_KEY=ignored"},
		{"suffix", "CAPY_DB_KEYS=ignored\nCAPY_DB_KEY_EXTRA=ignored\nCAPY_DB_KEY1=ignored"},
		{"prefix", "MY_CAPY_DB_KEY=ignored\n_CAPY_DB_KEY=ignored"},
		{"misspelled", "capy_db_key=ignored\nCAPY_DB_KE=ignored"},
		{"not_export", "exportCAPY_DB_KEY=ignored\nexported CAPY_DB_KEY=ignored"},
		{"not_line_start", "echo CAPY_DB_KEY=ignored\nOTHER='CAPY_DB_KEY=ignored'"},
		{"unrelated_shell", "source .envrc\nOTHER=$(never-run)\nAPP=\"multiline\nvalue\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, found, err := parseDotenvKey([]byte(tc.input), "/fixture/.env")
			require.NoError(t, err)
			assert.False(t, found)
			assert.Empty(t, key)
		})
	}
}

func TestParseDotenvKeyRejectsWholeFile(t *testing.T) {
	t.Parallel()
	const secret = "synthetic-marker-secret"
	valid := "CAPY_DB_KEY=" + secret
	for _, tc := range []struct {
		name, input, reason string
		line                int
	}{
		{"empty", "CAPY_DB_KEY=", "empty", 1},
		{"empty_single", "CAPY_DB_KEY=''", "empty", 1},
		{"empty_double", `CAPY_DB_KEY=""`, "empty", 1},
		{"comment_empty", "CAPY_DB_KEY= \t# ignored", "empty", 1},
		{"missing_equals", "export CAPY_DB_KEY " + secret, "assignment", 1},
		{"bare_name", "CAPY_DB_KEY", "assignment", 1},
		{"append", "CAPY_DB_KEY+=" + secret, "assignment", 1},
		{"colon", "CAPY_DB_KEY: " + secret, "assignment", 1},
		{"duplicate", valid + "\nexport CAPY_DB_KEY=" + secret, "duplicate", 2},
		{"malformed_duplicate", valid + "\nCAPY_DB_KEY ?", "assignment", 2},
		{"unquoted_spaces", valid + " extra", "whitespace", 1},
		{"unquoted_unicode_space", valid + "\u00a0extra", "unquoted", 1},
		{"unquoted_quote", valid + "'extra'", "unquoted", 1},
		{"unquoted_escape", valid + "\\x", "unquoted", 1},
		{"unquoted_expansion", valid + "$VAR", "expansion", 1},
		{"unquoted_backticks", valid + "`id`", "unquoted", 1},
		{"command_substitution", "CAPY_DB_KEY=$(touch sentinel)", "whitespace", 1},
		{"double_expansion", `CAPY_DB_KEY="` + secret + `$VAR"`, "expansion", 1},
		{"double_backticks", "CAPY_DB_KEY=\"" + secret + "`id`\"", "expansion", 1},
		{"double_escape_n", `CAPY_DB_KEY="` + secret + `\n"`, "escape", 1},
		{"double_escape_dollar", `CAPY_DB_KEY="` + secret + `\$VAR"`, "escape", 1},
		{"double_escape_quote_end", `CAPY_DB_KEY="` + secret + `\"`, "unterminated", 1},
		{"single_multiline", "CAPY_DB_KEY='" + secret + "\nmore'", "unterminated", 1},
		{"double_multiline", "CAPY_DB_KEY=\"" + secret + "\nmore\"", "unterminated", 1},
		{"embedded_multiline", "APP='begin\n" + valid + "\nend'", "unterminated", 1},
		{"embedded_heredoc", "cat <<EOF\n" + valid + "\nEOF", "assignment", 1},
		{"embedded_function", "fn() {\n" + valid + "\n}", "assignment", 1},
		{"executable_before", "source secret-script\n" + valid, "assignment", 1},
		{"executable_after", valid + "\necho secret-script", "assignment", 2},
		{"multiline_after", valid + "\nAPP='begin\nend'", "unterminated", 2},
		{"bad_name_after", valid + "\n1APP=value", "assignment", 2},
		{"trailing_statement", "CAPY_DB_KEY='" + secret + "'; echo extra", "after quoted", 1},
		{"concatenated_quotes", "CAPY_DB_KEY='" + secret + "'\"extra\"", "after quoted", 1},
		{"nul", valid + "\x00", "unquoted", 1},
		{"quoted_nul", "CAPY_DB_KEY='" + secret + "\x00'", "NUL", 1},
		{"bare_cr", valid + "\r", "unquoted", 1},
		{"embedded_cr", "CAPY_DB_KEY='" + secret + "\rmore'", "line endings", 1},
		{"invalid_non_target_escape_structure", valid + "\nAPP=\"unterminated\\", "unterminated", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, found, err := parseDotenvKey([]byte(tc.input), "/fixture/.env")
			require.Error(t, err)
			assert.Empty(t, key, "never return a partial credential")
			assert.False(t, found)
			assert.Contains(t, err.Error(), tc.reason)
			assert.Contains(t, err.Error(), fmt.Sprintf("line %d:", tc.line))
			assert.Contains(t, err.Error(), "/fixture/.env")
			assert.Contains(t, err.Error(), "store.key_file")
			assert.NotContains(t, err.Error(), secret)
			assert.NotContains(t, err.Error(), "secret-script")
		})
	}
	for _, operator := range []string{";", "&", "|", "<", ">", "(", ")"} {
		t.Run("operator_"+operator, func(t *testing.T) {
			key, found, err := parseDotenvKey([]byte(valid+operator+"extra"), "/fixture/.env")
			require.Error(t, err)
			assert.Empty(t, key)
			assert.False(t, found)
		})
	}
}
