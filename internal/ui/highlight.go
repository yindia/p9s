package ui

import (
	"bytes"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
)

const chromaStyle = "monokai"

// lexerAliases maps section languages to chroma lexers. Chroma has no CEL
// lexer; CEL's C-like syntax reads well with the JavaScript lexer.
var lexerAliases = map[string]string{
	"rego": "rego",
	"yaml": "yaml",
	"cel":  "javascript",
}

// highlighter colors source code. A disabled highlighter returns input as is.
type highlighter struct {
	enabled bool
}

func (h highlighter) highlight(code, lang string) string {
	if !h.enabled || code == "" {
		return code
	}
	name, ok := lexerAliases[strings.ToLower(lang)]
	if !ok {
		return code
	}
	lexer := lexers.Get(name)
	if lexer == nil {
		return code
	}
	lexer = chroma.Coalesce(lexer)
	it, err := lexer.Tokenise(nil, code)
	if err != nil {
		return code
	}
	var buf bytes.Buffer
	if err := formatters.Get("terminal256").Format(&buf, chromastyles.Get(chromaStyle), it); err != nil {
		return code
	}
	return strings.TrimRight(buf.String(), "\n")
}
