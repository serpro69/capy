package sanitize

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestStreamEverySecretPartition(t *testing.T) {
	secrets := []string{
		"sk-ant-abcdefghijklmnopqrstuvwxyz", "github_pat_ABCDEFGHIJKLMNOPQRSTUV", "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij",
		"xoxb-123456789012-abcdefghij", "AKIAIOSFODNN7EXAMPLE", "AIzaSyC-abcdefghijklmnopqrstuvwxyz12345",
		"npm_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij", "glpat-abcdefghijklmnopqrstuv", "dop_v1_" + strings.Repeat("A", 64),
		"eyJabcdefghijk.abcdefghijk.abcdefghijk", "pk-" + strings.Repeat("B", 30), "rk-" + strings.Repeat("C", 30), "ak-" + strings.Repeat("D", 30),
		"api_key = " + strings.Repeat("z", 30), "auth_credential: '" + strings.Repeat("q", 30) + "'",
		"<pRiVaTe>private contents\n<private>nested</PrIvAtE>",
	}
	for _, secret := range secrets {
		for split := 0; split <= len(secret); split++ {
			s := NewStream()
			var out strings.Builder
			for _, part := range []string{secret[:split], secret[split:]} {
				spans, err := s.Feed([]byte(part), false)
				require.NoError(t, err)
				for _, span := range spans {
					out.WriteString(span.Text)
				}
				checkpoint, err := s.Checkpoint()
				require.NoError(t, err)
				require.LessOrEqual(t, len(checkpoint), MaxCheckpointBytes)
				s, err = RestoreStream(checkpoint)
				require.NoError(t, err)
			}
			spans, err := s.Feed(nil, true)
			require.NoError(t, err)
			for _, span := range spans {
				out.WriteString(span.Text)
			}
			require.NotContains(t, out.String(), secret, "split %d", split)
			visible := strings.ReplaceAll(strings.ReplaceAll(out.String(), RedactedSecret, ""), RedactedPrivate, "")
			require.Empty(t, strings.Trim(visible, "\"'"), "split %d of %q leaked %q", split, secret, out.String())
			for _, span := range strings.Split(out.String(), RedactedSecret) {
				require.NotContains(t, span, "private contents")
			}
			require.EqualValues(t, len(secret), s.Covered)
		}
	}
}

func TestStreamUnboundedCandidatesAndUnfinishedBlocks(t *testing.T) {
	for _, prefix := range []string{"sk-", "token" + strings.Repeat(" ", 20000) + "=" + strings.Repeat(" ", 20000), "<private>"} {
		s := NewStream()
		input := prefix + strings.Repeat("Z", 100000) + " unfinished"
		for i := 0; i < len(input); i += 17 {
			spans, err := s.Feed([]byte(input[i:min(i+17, len(input))]), false)
			require.NoError(t, err)
			for _, span := range spans {
				require.NotContains(t, span.Text, "ZZZ")
			}
			checkpoint, err := s.Checkpoint()
			require.NoError(t, err)
			require.Less(t, len(checkpoint), 512)
		}
		spans, err := s.Feed(nil, true)
		require.NoError(t, err)
		for _, span := range spans {
			require.NotContains(t, span.Text, "ZZZ")
		}
	}
}

func TestStreamUTF8AndStateFailure(t *testing.T) {
	input := "Hello € 世界!\n"
	s := NewStream()
	var got strings.Builder
	for _, b := range []byte(input) {
		spans, err := s.Feed([]byte{b}, false)
		require.NoError(t, err)
		for _, span := range spans {
			got.WriteString(span.Text)
		}
	}
	spans, err := s.Feed(nil, true)
	require.NoError(t, err)
	for _, span := range spans {
		got.WriteString(span.Text)
	}
	require.Equal(t, input, got.String())
	for _, bad := range []string{`{}`, `{"version":99}`, strings.Repeat("x", 4097)} {
		_, err := RestoreStream([]byte(bad))
		require.Error(t, err)
	}
	s = NewStream()
	s.Version = 999
	_, err = s.Feed([]byte("private tail"), false)
	require.Error(t, err)
}

func TestStreamConservativeForOverlapsAndUnicodeFolding(t *testing.T) {
	for _, secret := range []string{
		"ſecret=" + strings.Repeat("A", 30), "paſſword=" + strings.Repeat("A", 30), "api_Key=" + strings.Repeat("A", 30),
		"secretoken=" + strings.Repeat("A", 30), "passwordop_v1_" + strings.Repeat("A", 64),
		"sk-token = " + strings.Repeat("A", 30), "sk-token=" + strings.Repeat("A", 30),
	} {
		require.NotContains(t, StripSecrets(secret), strings.Repeat("A", 30), "reference must protect fixture")
		for size := 1; size <= len(secret); size++ {
			s := NewStream()
			var out strings.Builder
			for i := 0; i < len(secret); i += size {
				spans, err := s.Feed([]byte(secret[i:min(i+size, len(secret))]), false)
				require.NoError(t, err)
				for _, span := range spans {
					out.WriteString(span.Text)
				}
			}
			spans, err := s.Feed(nil, true)
			require.NoError(t, err)
			for _, span := range spans {
				out.WriteString(span.Text)
			}
			require.NotContains(t, strings.ReplaceAll(out.String(), RedactedSecret, ""), "A", "partition size %d: %q", size, secret)
		}
	}
}

func BenchmarkStream(b *testing.B) {
	for _, mode := range []string{"plain", "secret", "private"} {
		b.Run(mode, func(b *testing.B) {
			state := NewStream()
			prefix := map[string]string{"secret": "sk-", "private": "<private>"}[mode]
			if _, err := state.Feed([]byte(prefix), false); err != nil {
				b.Fatal(err)
			}
			block := []byte(strings.Repeat("X", 16384))
			b.SetBytes(int64(len(block)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := state.Feed(block, false); err != nil {
					b.Fatal(err)
				}
			}
			checkpoint, err := state.Checkpoint()
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(len(checkpoint)), "checkpoint_B")
		})
	}
}
