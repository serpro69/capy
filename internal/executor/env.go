package executor

import (
	"os"
	"strings"
)

// deniedEnvVars is the set of dangerous environment variables stripped from
// the execution sandbox. Ported from context-mode/src/executor.ts lines 325-394.
var deniedEnvVars = map[string]bool{
	// Shell injection
	"BASH_ENV": true, "ENV": true, "PROMPT_COMMAND": true,
	"PS4": true, "SHELLOPTS": true, "BASHOPTS": true,
	"CDPATH": true, "INPUTRC": true, "BASH_XTRACEFD": true,
	// Node.js
	"NODE_OPTIONS": true, "NODE_PATH": true,
	// Python
	"PYTHONSTARTUP": true, "PYTHONHOME": true,
	"PYTHONWARNINGS": true, "PYTHONBREAKPOINT": true, "PYTHONINSPECT": true,
	// Ruby
	"RUBYOPT": true, "RUBYLIB": true,
	// Perl
	"PERL5OPT": true, "PERL5LIB": true, "PERLLIB": true, "PERL5DB": true,
	// Elixir/Erlang
	"ERL_AFLAGS": true, "ERL_FLAGS": true,
	"ELIXIR_ERL_OPTIONS": true, "ERL_LIBS": true,
	// Go
	"GOFLAGS": true, "CGO_CFLAGS": true, "CGO_LDFLAGS": true,
	// Rust
	"RUSTC": true, "RUSTC_WRAPPER": true, "RUSTC_WORKSPACE_WRAPPER": true,
	"CARGO_BUILD_RUSTC": true, "CARGO_BUILD_RUSTC_WRAPPER": true, "RUSTFLAGS": true,
	// PHP
	"PHPRC": true, "PHP_INI_SCAN_DIR": true,
	// R
	"R_PROFILE": true, "R_PROFILE_USER": true, "R_HOME": true,
	// Shared library injection
	"LD_PRELOAD": true, "DYLD_INSERT_LIBRARIES": true,
	// OpenSSL
	"OPENSSL_CONF": true, "OPENSSL_ENGINES": true,
	// Compilers
	"CC": true, "CXX": true, "AR": true,
	// Git
	"GIT_TEMPLATE_DIR": true, "GIT_CONFIG_GLOBAL": true,
	"GIT_CONFIG_SYSTEM": true, "GIT_EXEC_PATH": true,
	"GIT_SSH": true, "GIT_SSH_COMMAND": true, "GIT_ASKPASS": true,
}

// sslCertPaths lists common CA bundle locations for SSL cert detection.
var sslCertPaths = []string{
	"/etc/ssl/cert.pem",
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
}

// BuildSafeEnv returns an environment for sandboxed execution.
// It strips dangerous vars, applies sandbox overrides, and detects SSL certs.
func BuildSafeEnv(tmpDir string) []string {
	realHome := os.Getenv("HOME")

	env := make(map[string]string)
	for _, entry := range os.Environ() {
		key, val, _ := strings.Cut(entry, "=")
		if deniedEnvVars[key] || strings.HasPrefix(key, "BASH_FUNC_") {
			continue
		}
		env[key] = val
	}

	// Sandbox overrides.
	env["TMPDIR"] = tmpDir
	env["HOME"] = realHome
	env["LANG"] = "en_US.UTF-8"
	env["PYTHONDONTWRITEBYTECODE"] = "1"
	env["PYTHONUNBUFFERED"] = "1"
	env["PYTHONUTF8"] = "1"
	env["NO_COLOR"] = "1"

	if env["PATH"] == "" {
		env["PATH"] = "/usr/local/bin:/usr/bin:/bin"
	}

	// SSL cert detection.
	if env["SSL_CERT_FILE"] == "" {
		for _, p := range sslCertPaths {
			if _, err := os.Stat(p); err == nil {
				env["SSL_CERT_FILE"] = p
				break
			}
		}
	}

	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}
