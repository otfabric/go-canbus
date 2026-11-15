// SPDX-License-Identifier: MIT

package dbc

import (
	"fmt"
	"strings"
)

type tokKind uint8

const (
	tEOF tokKind = iota
	tIdent
	tNumber
	tString
	tPunct
)

func (k tokKind) String() string {
	return [...]string{"end of file", "identifier", "number", "string", "punctuation"}[k]
}

type token struct {
	kind tokKind
	text string
	line int
	col  int // 0 for the first character of a line
}

func (t token) String() string {
	switch t.kind {
	case tEOF:
		return "end of file"
	case tString:
		return fmt.Sprintf("string %q", t.text)
	default:
	}
	return fmt.Sprintf("%q", t.text)
}

// lexer splits DBC source into tokens, one at a time, so the parser never
// holds more than the token it looks at. Identifiers are C identifiers;
// numbers are unsigned decimal integers or floats with an optional
// exponent, or 0x hexadecimal (signs are separate tokens); strings are
// double quoted with backslash escapes and may span lines; '//' starts a
// comment to the end of the line.
type lexer struct {
	src       string
	i         int // offset of the next byte
	line      int // line of the next byte, from 1
	lineStart int // offset of the first byte of that line
}

func newLexer(src string) *lexer { return &lexer{src: src, line: 1} }

// check lexes the whole source and returns its first error. The parser
// runs only on source that passed, so its own lexer cannot fail.
func check(src string) error {
	l := newLexer(src)
	for {
		t, err := l.next(false)
		if err != nil || t.kind == tEOF {
			return err
		}
	}
}

// next returns the next token; at the end of the source the end-of-file
// token, again on every further call. With text false the text of a string
// token is not built.
func (l *lexer) next(text bool) (token, error) {
	src := l.src
	for l.i < len(src) {
		c := src[l.i]
		switch {
		case c == '\n':
			l.line++
			l.i++
			l.lineStart = l.i
			continue
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			l.i++
			continue
		case c == '/' && l.i+1 < len(src) && src[l.i+1] == '/':
			for l.i < len(src) && src[l.i] != '\n' {
				l.i++
			}
			continue
		}
		i := l.i
		start := i
		tok := token{line: l.line, col: i - l.lineStart}
		switch {
		case isIdentStart(c):
			for i < len(src) && isIdentPart(src[i]) {
				i++
			}
			tok.kind, tok.text = tIdent, src[start:i]
		case c >= '0' && c <= '9' || c == '.' && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9':
			if c == '0' && i+1 < len(src) && (src[i+1] == 'x' || src[i+1] == 'X') {
				i += 2
				for i < len(src) && isHex(src[i]) {
					i++
				}
			} else {
				for i < len(src) && (src[i] >= '0' && src[i] <= '9' || src[i] == '.') {
					i++
				}
				if i < len(src) && (src[i] == 'e' || src[i] == 'E') {
					j := i + 1
					if j < len(src) && (src[j] == '+' || src[j] == '-') {
						j++
					}
					if j < len(src) && src[j] >= '0' && src[j] <= '9' {
						for j < len(src) && src[j] >= '0' && src[j] <= '9' {
							j++
						}
						i = j
					}
				}
			}
			tok.kind, tok.text = tNumber, src[start:i]
			// A number glued to letters ("1abc") is an identifier-like
			// token some tools write for node names; keep it whole.
			if i < len(src) && isIdentStart(src[i]) {
				for i < len(src) && isIdentPart(src[i]) {
					i++
				}
				tok.kind, tok.text = tIdent, src[start:i]
			}
		case c == '"':
			var b strings.Builder
			i++
			closed := false
			for i < len(src) {
				ch := src[i]
				if ch == '\\' && i+1 < len(src) {
					if text {
						b.WriteByte(src[i+1])
					}
					i += 2
					continue
				}
				if ch == '"' {
					closed = true
					i++
					break
				}
				if ch == '\n' {
					l.line++
					l.lineStart = i + 1
				}
				if text {
					b.WriteByte(ch)
				}
				i++
			}
			if !closed {
				return token{}, fmt.Errorf("line %d: unterminated string", tok.line)
			}
			tok.kind, tok.text = tString, b.String()
		case strings.IndexByte(":;,|@()[]+-{}", c) >= 0:
			i++
			tok.kind, tok.text = tPunct, src[start:i]
		default:
			return token{}, fmt.Errorf("line %d: unexpected character %q", l.line, c)
		}
		l.i = i
		return tok, nil
	}
	return token{kind: tEOF, line: l.line}, nil
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func isIdentPart(c byte) bool { return isIdentStart(c) || c >= '0' && c <= '9' }

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}
