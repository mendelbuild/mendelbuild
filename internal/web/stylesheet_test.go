package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The stylesheets have to parse as CSS.
//
// This is here because they once did not. A scripted edit inserted a rule
// between the two halves of a descendant selector, leaving `a.callout` joined
// to an unrelated selector by a comment and `.callout-title` promoted to a rule
// of its own. Nothing failed: browsers discard what they cannot parse and apply
// what they can, so the result was every callout title in the app rendered blue
// and underlined, including the ones that link nowhere. It reached a deployment
// and was found by eye.
//
// A full CSS parser would be overkill. These check the two things that actually
// went wrong: braces that do not balance, and a selector containing something
// no selector may contain.
func TestStylesheetsAreWellFormed(t *testing.T) {
	for _, name := range []string{"tokens.css", "components.css"} {
		path := filepath.Join("static", "css", name)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		text := string(body)

		if open, close := countOutsideComments(text); open != close {
			t.Errorf("%s: %d opening braces and %d closing ones", name, open, close)
		}

		// Walk the top level and check what precedes each block.
		//
		// Braces inside a comment are prose, not structure: a comment quoting a
		// rule -- to explain what it has to outweigh, say -- is ordinary and must
		// not be read as opening one. The selector text handed to checkSelector
		// still includes comments, because a comment *inside* a selector is
		// exactly the corruption this exists to catch.
		depth, selStart, inComment := 0, 0, false
		for i := 0; i < len(text); i++ {
			if inComment {
				if text[i] == '*' && i+1 < len(text) && text[i+1] == '/' {
					inComment = false
					i++
				}
				continue
			}
			if text[i] == '/' && i+1 < len(text) && text[i+1] == '*' {
				inComment = true
				i++
				continue
			}
			switch text[i] {
			case '{':
				if depth == 0 {
					checkSelector(t, name, text[selStart:i], lineOf(text, i))
				}
				depth++
			case '}':
				depth--
				if depth == 0 {
					selStart = i + 1
				}
			}
		}
	}
}

// checkSelector rejects the shapes that mean an edit went wrong. A comment is
// allowed before a selector but not inside it: `a.callout /* ... */ .stack > x`
// is what the corruption looked like, and it is indistinguishable from a
// deliberate descendant selector unless you notice the comment closes after the
// combinator rather than before it.
func checkSelector(t *testing.T, file, raw string, line int) {
	t.Helper()

	// Strip whole leading comments, which are ordinary and expected.
	sel := raw
	for {
		start := strings.Index(sel, "/*")
		if start == -1 {
			break
		}
		end := strings.Index(sel[start:], "*/")
		if end == -1 {
			t.Errorf("%s:%d: unterminated comment before a rule", file, line)
			return
		}
		before := strings.TrimSpace(sel[:start])
		after := sel[start+end+2:]
		if before != "" && strings.TrimSpace(after) != "" {
			t.Errorf("%s:%d: a comment sits inside the selector %q; "+
				"an edit has spliced two rules together",
				file, line, strings.Join(strings.Fields(raw), " "))
			return
		}
		sel = after
	}

	sel = strings.TrimSpace(sel)
	if sel == "" {
		t.Errorf("%s:%d: a block with no selector", file, line)
	}
	if strings.Contains(sel, ";") {
		t.Errorf("%s:%d: %q contains a semicolon; a declaration has escaped its block",
			file, line, strings.Join(strings.Fields(sel), " "))
	}
}

func lineOf(text string, offset int) int {
	return strings.Count(text[:offset], "\n") + 1
}


// countOutsideComments counts braces that structure the stylesheet, ignoring any
// a comment happens to quote.
func countOutsideComments(text string) (open, close int) {
	inComment := false
	for i := 0; i < len(text); i++ {
		if inComment {
			if text[i] == '*' && i+1 < len(text) && text[i+1] == '/' {
				inComment = false
				i++
			}
			continue
		}
		if text[i] == '/' && i+1 < len(text) && text[i+1] == '*' {
			inComment = true
			i++
			continue
		}
		switch text[i] {
		case '{':
			open++
		case '}':
			close++
		}
	}
	return open, close
}
