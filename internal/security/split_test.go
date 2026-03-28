package security

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSplitChainedCommands(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    []string
	}{
		{
			"simple &&",
			"echo hello && sudo rm -rf /",
			[]string{"echo hello", "sudo rm -rf /"},
		},
		{
			"simple ||",
			"test -f foo || echo missing",
			[]string{"test -f foo", "echo missing"},
		},
		{
			"semicolon",
			"cd /tmp; ls",
			[]string{"cd /tmp", "ls"},
		},
		{
			"pipe",
			"cat file | grep error",
			[]string{"cat file", "grep error"},
		},
		{
			"mixed operators",
			"echo a && echo b || echo c; echo d | cat",
			[]string{"echo a", "echo b", "echo c", "echo d", "cat"},
		},
		{
			"single quoted && preserved",
			"echo 'hello && world' && sudo rm",
			[]string{"echo 'hello && world'", "sudo rm"},
		},
		{
			"double quoted || preserved",
			`echo "a || b" || fail`,
			[]string{`echo "a || b"`, "fail"},
		},
		{
			"backtick quoted ; preserved",
			"echo `date; time` ; ls",
			[]string{"echo `date; time`", "ls"},
		},
		{
			"no operators",
			"echo hello world",
			[]string{"echo hello world"},
		},
		{
			"empty result filtered",
			"&& echo hello",
			[]string{"echo hello"},
		},
		{
			"escaped quote",
			`echo "test \" && inner" && real`,
			[]string{`echo "test \" && inner"`, "real"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SplitChainedCommands(tt.command))
		})
	}
}
