package sanitize

import (
	"strings"
	"testing"
)

func TestStripGenericKeyValue(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"equals", `api_key=sk-abc123def456ghi789jkl012mno`},
		{"colon", `secret: "mySecretValue12345678901234"`},
		{"token", `token=abcdefghij1234567890klmno`},
		{"password", `password=SuperSecretPassword12345`},
		{"credential", `credential: longCredentialValue12345678`},
		{"api-key-dash", `api-key=someAPIKey12345678901234`},
		{"auth-token", `auth_token=SomeAuthTokenValue1234567890`},
		{"auth-key", `auth-key=SomeAuthKeyValueThat1234567890`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripSecrets(tt.input)
			if !strings.Contains(got, RedactedSecret) {
				t.Errorf("expected redaction in %q, got %q", tt.input, got)
			}
		})
	}
}

func TestStripAnthropicKey(t *testing.T) {
	input := `key is sk-ant-api03-abcdefghijklmnopqrst here`
	got := StripSecrets(input)
	if strings.Contains(got, "sk-ant-api03") {
		t.Errorf("Anthropic key not redacted: %q", got)
	}
	if !strings.Contains(got, RedactedSecret) {
		t.Errorf("expected %s placeholder, got %q", RedactedSecret, got)
	}
}

func TestStripGitHubPAT(t *testing.T) {
	input := `export GITHUB_TOKEN=ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij`
	got := StripSecrets(input)
	if strings.Contains(got, "ghp_") {
		t.Errorf("GitHub PAT not redacted: %q", got)
	}
}

func TestStripGitHubFinegrainedPAT(t *testing.T) {
	input := `token: github_pat_ABCDEFGHIJKLMNOPQRSTUV`
	got := StripSecrets(input)
	if strings.Contains(got, "github_pat_") {
		t.Errorf("GitHub fine-grained PAT not redacted: %q", got)
	}
}

func TestStripSlackToken(t *testing.T) {
	input := `SLACK_TOKEN=xoxb-123456789012-abcdefghij`
	got := StripSecrets(input)
	if strings.Contains(got, "xoxb-") {
		t.Errorf("Slack token not redacted: %q", got)
	}
}

func TestSlackShortNotStripped(t *testing.T) {
	input := `xoxb-short`
	got := StripSecrets(input)
	if got != input {
		t.Errorf("short Slack fragment falsely redacted:\ninput:  %q\noutput: %q", input, got)
	}
}

func TestStripAWSKey(t *testing.T) {
	input := `aws_access_key_id = AKIAIOSFODNN7EXAMPLE`
	got := StripSecrets(input)
	if strings.Contains(got, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("AWS key not redacted: %q", got)
	}
}

func TestStripGoogleAPIKey(t *testing.T) {
	input := `GOOGLE_API_KEY=AIzaSyC-abcdefghijklmnopqrstuvwxyz12345`
	got := StripSecrets(input)
	if strings.Contains(got, "AIzaSyC") {
		t.Errorf("Google API key not redacted: %q", got)
	}
}

func TestStripJWT(t *testing.T) {
	input := `Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U`
	got := StripSecrets(input)
	if strings.Contains(got, "eyJhbGci") {
		t.Errorf("JWT not redacted: %q", got)
	}
}

func TestStripNpmToken(t *testing.T) {
	input := `//registry.npmjs.org/:_authToken=npm_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij`
	got := StripSecrets(input)
	if strings.Contains(got, "npm_") {
		t.Errorf("npm token not redacted: %q", got)
	}
}

func TestStripGitLabToken(t *testing.T) {
	input := `GITLAB_TOKEN=glpat-abcdefghijklmnopqrstuv`
	got := StripSecrets(input)
	if strings.Contains(got, "glpat-") {
		t.Errorf("GitLab token not redacted: %q", got)
	}
}

func TestStripDigitalOceanToken(t *testing.T) {
	input := `DO_TOKEN=dop_v1_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789AB`
	got := StripSecrets(input)
	if strings.Contains(got, "dop_v1_") {
		t.Errorf("DigitalOcean token not redacted: %q", got)
	}
}

func TestStripGenericPrefixedToken(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
	}{
		{"sk", "sk-"},
		{"pk", "pk-"},
		{"rk", "rk-"},
		{"ak", "ak-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := tt.prefix + "ABCDEFGHIJKLMNOPQRSTUVWXYZab"
			got := StripSecrets(input)
			if strings.Contains(got, tt.prefix) {
				t.Errorf("prefixed token %s not redacted: %q", tt.prefix, got)
			}
		})
	}
}

func TestStripPrivateTag(t *testing.T) {
	input := `before <private>secret data here</private> after`
	got := StripSecrets(input)
	if strings.Contains(got, "secret data") {
		t.Errorf("private tag content not redacted: %q", got)
	}
	if !strings.Contains(got, RedactedPrivate) {
		t.Errorf("expected %s placeholder, got %q", RedactedPrivate, got)
	}
	if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Errorf("surrounding text should be preserved: %q", got)
	}
}

func TestStripPrivateTagCaseInsensitive(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"all caps", `<PRIVATE>hidden</PRIVATE>`},
		{"title case", `<Private>hidden</Private>`},
		{"mixed case", `<pRiVaTe>hidden</pRiVaTe>`},
		{"upper lower mix", `<priVATE>hidden</priVATE>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripSecrets(tt.input)
			if strings.Contains(got, "hidden") {
				t.Errorf("case-insensitive private tag not redacted: %q", got)
			}
			if !strings.Contains(got, RedactedPrivate) {
				t.Errorf("expected %s placeholder, got %q", RedactedPrivate, got)
			}
		})
	}
}

func TestStripPrivateTagMultiline(t *testing.T) {
	input := "<private>\nline1\nline2\n</private>"
	got := StripSecrets(input)
	if strings.Contains(got, "line1") {
		t.Errorf("multiline private tag not redacted: %q", got)
	}
}

func TestStripMultiplePatterns(t *testing.T) {
	input := `
api_key=sk-abc123def456ghi789jkl012mno
GITHUB_TOKEN=ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij
<private>internal note</private>
`
	got := StripSecrets(input)
	if strings.Contains(got, "sk-abc123") {
		t.Errorf("generic key not redacted")
	}
	if strings.Contains(got, "ghp_") {
		t.Errorf("GitHub PAT not redacted")
	}
	if strings.Contains(got, "internal note") {
		t.Errorf("private tag not redacted")
	}
}

func TestBareAuthNotStripped(t *testing.T) {
	// "auth" alone should not trigger the generic pattern — only auth_token, auth_key, etc.
	input := `auth=SomeAuthTokenValue1234567890`
	got := StripSecrets(input)
	if got != input {
		t.Errorf("bare 'auth=' falsely redacted:\ninput:  %q\noutput: %q", input, got)
	}
}

func TestPreservesNonSecrets(t *testing.T) {
	inputs := []string{
		"func main() { fmt.Println(\"hello\") }",
		"SELECT * FROM users WHERE id = 1",
		"This is a normal paragraph with no secrets.",
		"version: 1.0.0\nname: my-app",
		"The quick brown fox jumps over the lazy dog",
	}
	for _, input := range inputs {
		got := StripSecrets(input)
		if got != input {
			t.Errorf("non-secret content modified:\ninput:  %q\noutput: %q", input, got)
		}
	}
}

func TestShortTokensNotStripped(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"short sk prefix", "sk-abc"},
		{"short pk prefix", "pk-short"},
		{"short ghp", "ghp_short"},
		{"short npm", "npm_short"},
		{"short glpat", "glpat-short"},
		{"two-segment jwt-like", "eyJhbGci.eyJzdWI"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripSecrets(tt.input)
			if got != tt.input {
				t.Errorf("short token falsely redacted:\ninput:  %q\noutput: %q", tt.input, got)
			}
		})
	}
}

func TestEmptyContent(t *testing.T) {
	got := StripSecrets("")
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestStripSecretsIdempotent(t *testing.T) {
	input := `secret=ABCDEFGHIJKLMNOPQRSTUVwxyz1234`
	first := StripSecrets(input)
	second := StripSecrets(first)
	if first != second {
		t.Errorf("StripSecrets not idempotent:\nfirst:  %q\nsecond: %q", first, second)
	}
}
