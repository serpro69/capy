package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// autoWrap applies language-specific boilerplate wrapping.
func autoWrap(lang Language, code, projectDir string) string {
	switch lang {
	case Go:
		if !strings.Contains(code, "package ") {
			return fmt.Sprintf("package main\n\nimport \"fmt\"\n\nfunc main() {\n%s\n}\n", code)
		}
	case PHP:
		if !strings.HasPrefix(strings.TrimSpace(code), "<?") {
			return "<?php\n" + code
		}
	case Elixir:
		if mixExists(projectDir) {
			return `Path.wildcard("_build/dev/lib/*/ebin") |> Enum.each(&Code.prepend_path/1)` + "\n" + code
		}
	}
	return code
}

func mixExists(projectDir string) bool {
	_, err := os.Stat(filepath.Join(projectDir, "mix.exs"))
	return err == nil
}

// injectFileContent prepends file-reading boilerplate for execute_file.
func injectFileContent(lang Language, code, absPath string) string {
	escaped := strconv.Quote(absPath)
	switch lang {
	case JavaScript, TypeScript:
		return fmt.Sprintf("const FILE_CONTENT_PATH = %s;\nconst file_path = FILE_CONTENT_PATH;\nconst FILE_CONTENT = require(\"fs\").readFileSync(FILE_CONTENT_PATH, \"utf-8\");\n%s", escaped, code)
	case Python:
		return fmt.Sprintf("FILE_CONTENT_PATH = %s\nfile_path = FILE_CONTENT_PATH\nwith open(FILE_CONTENT_PATH, \"r\", encoding=\"utf-8\") as _f:\n    FILE_CONTENT = _f.read()\n%s", escaped, code)
	case Shell:
		sq := "'" + strings.ReplaceAll(absPath, "'", "'\\''") + "'"
		return fmt.Sprintf("FILE_CONTENT_PATH=%s\nfile_path=%s\nFILE_CONTENT=$(cat %s)\n%s", sq, sq, sq, code)
	case Ruby:
		return fmt.Sprintf("FILE_CONTENT_PATH = %s\nfile_path = FILE_CONTENT_PATH\nFILE_CONTENT = File.read(FILE_CONTENT_PATH, encoding: \"utf-8\")\n%s", escaped, code)
	case Go:
		return fmt.Sprintf("package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nvar FILE_CONTENT_PATH = %s\nvar file_path = FILE_CONTENT_PATH\n\nfunc main() {\n\tb, _ := os.ReadFile(FILE_CONTENT_PATH)\n\tFILE_CONTENT := string(b)\n\t_ = FILE_CONTENT\n\t_ = fmt.Sprint()\n%s\n}\n", escaped, code)
	case Rust:
		return fmt.Sprintf("#![allow(unused_variables)]\nuse std::fs;\n\nfn main() {\n    let file_content_path = %s;\n    let file_path = file_content_path;\n    let file_content = fs::read_to_string(file_content_path).unwrap();\n%s\n}\n", escaped, code)
	case PHP:
		return fmt.Sprintf("<?php\n$FILE_CONTENT_PATH = %s;\n$file_path = $FILE_CONTENT_PATH;\n$FILE_CONTENT = file_get_contents($FILE_CONTENT_PATH);\n%s", escaped, code)
	case Perl:
		return fmt.Sprintf("my $FILE_CONTENT_PATH = %s;\nmy $file_path = $FILE_CONTENT_PATH;\nopen(my $fh, '<:encoding(UTF-8)', $FILE_CONTENT_PATH) or die \"Cannot open: $!\";\nmy $FILE_CONTENT = do { local $/; <$fh> };\nclose($fh);\n%s", escaped, code)
	case R:
		return fmt.Sprintf("FILE_CONTENT_PATH <- %s\nfile_path <- FILE_CONTENT_PATH\nFILE_CONTENT <- readLines(FILE_CONTENT_PATH, warn=FALSE, encoding=\"UTF-8\")\nFILE_CONTENT <- paste(FILE_CONTENT, collapse=\"\\n\")\n%s", escaped, code)
	case Elixir:
		return fmt.Sprintf("file_content_path = %s\nfile_path = file_content_path\nfile_content = File.read!(file_content_path)\n%s", escaped, code)
	}
	return code
}
