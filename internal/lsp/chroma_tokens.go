package lsp

import (
	"fmt"
	"os"

	chroma "github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// chromaTokeniseOptions disables EnsureLF so token Values are never rewritten
// relative to the raw file bytes. Structural sources track byte offsets by
// summing len(token.Value); those offsets must stay consistent with the exact
// text handed to OffsetForPosition/PositionForOffset, which operate on raw
// file bytes and never normalize CRLF themselves.
var chromaTokeniseOptions = &chroma.TokeniseOptions{State: "root", EnsureLF: false}

// chromaTokensForFile reads path and tokenizes it with the named chroma
// lexer. Returns the raw file text alongside the flat token stream so callers
// can walk tokens while tracking byte offsets into that same text.
func chromaTokensForFile(path, lexerName string) (string, []chroma.Token, error) {
	lexer := lexers.Get(lexerName)
	if lexer == nil {
		return "", nil, fmt.Errorf("chroma: no lexer registered for %q", lexerName)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	text := string(src)
	iter, err := lexer.Tokenise(chromaTokeniseOptions, text)
	if err != nil {
		return "", nil, err
	}
	return text, iter.Tokens(), nil
}
