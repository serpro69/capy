package sanitize

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

const StreamVersion = 1
const MaxCheckpointBytes = 4096

// Span is finalized projection with half-open raw byte anchors. Redacted spans
// may intentionally cover more than the whole-string recognizer would hide.
type Span struct {
	Start, End int64
	Text       string
	Redacted   bool
}

// Stream never publishes a partial recognizer prefix. Recognized prefixes are
// conservatively redacted immediately, avoiding buffers proportional to token,
// whitespace, JWT or private-block length. Each instance owns one raw stream.
type Stream struct {
	Version   int    `json:"version"`
	Position  int64  `json:"position"`
	Covered   int64  `json:"covered"`
	Mode      string `json:"mode"`
	Pending   []byte `json:"pending"`
	Closing   string `json:"closing"`
	Final     bool   `json:"final"`
	Separated bool   `json:"separated"`
}

func NewStream() *Stream { return &Stream{Version: StreamVersion, Mode: "plain"} }

type prefixRule struct {
	literal, mode string
	fold          bool
}

var streamPrefixes = []prefixRule{
	{"<private>", "private", true},
	{"sk-", "token", false}, {"pk-", "token", false}, {"rk-", "token", false}, {"ak-", "token", false},
	{"github_pat_", "token", false}, {"ghp_", "token", false}, {"xoxb-", "token", false},
	{"AKIA", "token", false}, {"AIza", "token", false}, {"npm_", "token", false},
	{"glpat-", "token", false}, {"dop_v1_", "token", false}, {"eyJ", "token", false},
	{"apikey", "key", true}, {"api_key", "key", true}, {"api-key", "key", true},
	{"secret", "key", true}, {"token", "key", true}, {"password", "key", true}, {"credential", "key", true},
	{"authtoken", "key", true}, {"auth_token", "key", true}, {"auth-token", "key", true},
	{"authkey", "key", true}, {"auth_key", "key", true}, {"auth-key", "key", true},
	{"authsecret", "key", true}, {"auth_secret", "key", true}, {"auth-secret", "key", true},
	{"authcredential", "key", true}, {"auth_credential", "key", true}, {"auth-credential", "key", true},
}

func (s *Stream) valid() bool {
	if s.Version != StreamVersion || s.Position < 0 || s.Covered < 0 || s.Covered > s.Position || len(s.Pending) > 32 || s.Position-s.Covered != int64(len(s.Pending)) || len(s.Closing) > 10 {
		return false
	}
	switch s.Mode {
	case "plain", "private", "token", "key", "value":
		return true
	default:
		return false
	}
}

func RestoreStream(checkpoint []byte) (*Stream, error) {
	if len(checkpoint) > MaxCheckpointBytes {
		return nil, errors.New("redaction checkpoint exceeds limit")
	}
	var s Stream
	d := json.NewDecoder(bytes.NewReader(checkpoint))
	d.DisallowUnknownFields()
	if d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF || !s.valid() {
		return nil, errors.New("redaction checkpoint unavailable")
	}
	return &s, nil
}

func (s *Stream) Checkpoint() ([]byte, error) {
	if !s.valid() {
		return nil, errors.New("redaction state unavailable")
	}
	return json.Marshal(s)
}

func tokenByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("_-.+/", rune(b))
}
func spaceByte(b byte) bool { return b == ' ' || b == '\t' || b == '\r' || b == '\n' || b == '\f' }

// Feed consumes a bounded input block. The caller persists spans and checkpoint
// with the corresponding raw bytes in one transaction. EOF never releases an
// unresolved candidate. Invalid UTF-8 is represented without exposing raw bytes.
func (s *Stream) Feed(data []byte, eof bool) ([]Span, error) {
	if !s.valid() || s.Final || len(data) > 16384 {
		return nil, errors.New("redaction state unavailable")
	}
	spans := make([]Span, 0, 8)
	emit := func(end int64, text string, redacted bool) {
		if end <= s.Covered {
			return
		}
		if len(spans) > 0 {
			last := &spans[len(spans)-1]
			if last.End == s.Covered && last.Redacted == redacted && (!redacted || last.Text == text) {
				last.End = end
				if !redacted {
					last.Text += text
				}
				s.Covered = end
				return
			}
		}
		spans = append(spans, Span{Start: s.Covered, End: end, Text: text, Redacted: redacted})
		s.Covered = end
	}
	var plain strings.Builder
	plainEnd := s.Covered
	flush := func() {
		if plain.Len() > 0 {
			emit(plainEnd, plain.String(), false)
			plain.Reset()
		}
	}
	for _, b := range data {
		s.Position++
		if s.Mode == "private" {
			flush()
			emit(s.Position, RedactedPrivate, true)
			lower := b
			if lower >= 'A' && lower <= 'Z' {
				lower += 'a' - 'A'
			}
			const closing = "</private>"
			switch {
			case len(s.Closing) < len(closing) && lower == closing[len(s.Closing)]:
				s.Closing = closing[:len(s.Closing)+1]
			case lower == '<':
				s.Closing = "<"
			default:
				s.Closing = ""
			}
			if s.Closing == closing {
				s.Mode = "plain"
				s.Closing = ""
			}

			continue
		}
		if s.Mode == "key" {
			if spaceByte(b) {
				s.Separated = true
				flush()
				emit(s.Position, RedactedSecret, true)
				continue
			}
			if b == '=' || b == ':' {
				flush()
				emit(s.Position, RedactedSecret, true)
				s.Mode = "value"
				continue
			}
			if s.Separated {
				s.Mode = "plain"
			} else {
				s.Mode = "token"
			}
		}
		if s.Mode == "value" {
			if spaceByte(b) || b == '"' || b == 39 {
				flush()
				emit(s.Position, RedactedSecret, true)
				continue
			}
			s.Mode = "token"
		}
		if s.Mode == "token" {
			if b == '=' || b == ':' {
				flush()
				emit(s.Position, RedactedSecret, true)
				s.Mode = "value"
				continue
			}
			if spaceByte(b) {
				flush()
				emit(s.Position, RedactedSecret, true)
				s.Mode = "key"
				s.Separated = true
				continue
			}
			if tokenByte(b) || b >= 128 {
				flush()
				emit(s.Position, RedactedSecret, true)
				continue
			}
			s.Mode = "plain"
		}
		s.Pending = append(s.Pending, b)
		for len(s.Pending) > 0 {
			complete := true
			for tail := s.Pending; len(tail) > 0; {
				if !utf8.FullRune(tail) {
					complete = false
					break
				}
				_, n := utf8.DecodeRune(tail)
				tail = tail[n:]
			}
			if !complete {
				break
			}
			pending := string(s.Pending)
			possible := false
			mode := ""
			for _, rule := range streamPrefixes {
				if pending[0] < utf8.RuneSelf {
					first := pending[0]
					if rule.fold && first >= 'A' && first <= 'Z' {
						first += 'a' - 'A'
					}
					if first != rule.literal[0] {
						continue
					}
				}
				size := len(pending)
				matches := strings.HasPrefix(rule.literal, pending)
				if rule.fold {
					size = utf8.RuneCountInString(pending)
					matches = size <= len(rule.literal) && strings.EqualFold(pending, rule.literal[:min(size, len(rule.literal))])
				}
				if matches {
					possible = true
					if size == len(rule.literal) {
						mode = rule.mode
						break
					}
				}
			}

			if mode != "" {
				flush()
				marker := RedactedSecret
				if mode == "private" {
					marker = RedactedPrivate
				}
				emit(s.Position, marker, true)
				s.Pending = s.Pending[:0]
				s.Mode = mode
				s.Separated = false
				break
			}
			if possible || !utf8.FullRune(s.Pending) {
				break
			}
			r, n := utf8.DecodeRune(s.Pending)
			end := s.Position - int64(len(s.Pending)) + int64(n)
			if r == utf8.RuneError && n == 1 {
				flush()
				emit(end, "�", true)
			} else {
				plain.Write(s.Pending[:n])
				plainEnd = end
			}
			copy(s.Pending, s.Pending[n:])
			s.Pending = s.Pending[:len(s.Pending)-n]
		}
	}
	flush()
	if eof {
		if len(s.Pending) > 0 {
			emit(s.Position, RedactedSecret, true)
			s.Pending = s.Pending[:0]
		}
		s.Final = true
	}
	if !s.valid() {
		return nil, errors.New("redaction state unavailable")
	}
	return spans, nil
}
