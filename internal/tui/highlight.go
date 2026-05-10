package tui

import (
	"path/filepath"
	"strings"

	chroma "github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// highlightFormatterName is the chroma formatter that emits 256-color
// ANSI sequences. Compatible with every modern terminal; degrades
// gracefully on 16-color terminals (chroma falls back internally).
const highlightFormatterName = "terminal256"

// highlightStyleName picks the chroma style. "monokai" reads well on
// the dark backgrounds most yottacode users have; on light terminals
// it's still readable. We don't expose this as user-configurable yet —
// add a flag later if real users want it.
const highlightStyleName = "monokai"

// HighlightCode runs source through chroma and returns ANSI-colored
// text. languageHint can be a chroma lexer name ("go", "python") or
// empty; in either case the helper does its best to detect from
// content if the hint doesn't match a known lexer. Falls back to
// plain text when chroma can't tokenize.
//
// Used by the approval modal renderers to make code review easier:
// the user sees keywords / strings / comments in distinct colors
// rather than a wall of monochrome text.
func HighlightCode(source, languageHint string) string {
	if source == "" {
		return source
	}
	lexer := pickLexer(languageHint, source)
	if lexer == nil {
		return source
	}
	style := styles.Get(highlightStyleName)
	if style == nil {
		style = styles.Fallback
	}
	formatter := formatters.Get(highlightFormatterName)
	if formatter == nil {
		formatter = formatters.Fallback
	}
	iterator, err := lexer.Tokenise(nil, source)
	if err != nil {
		return source
	}
	var b strings.Builder
	if err := formatter.Format(&b, style, iterator); err != nil {
		return source
	}
	return b.String()
}

// HighlightFromPath is the convenience form: derive the language hint
// from the file extension, then highlight. Used by approval renderers
// that have a path on hand (edit_file, write_file).
func HighlightFromPath(source, path string) string {
	return HighlightCode(source, languageFromPath(path))
}

// pickLexer tries a hint-based lookup first, then content analysis,
// then falls back. Returns nil if all paths fail (unusual — chroma's
// fallback lexer is always available).
func pickLexer(hint, source string) chroma.Lexer {
	if hint != "" {
		if l := lexers.Get(hint); l != nil {
			return chroma.Coalesce(l)
		}
	}
	if l := lexers.Analyse(source); l != nil {
		return chroma.Coalesce(l)
	}
	return chroma.Coalesce(lexers.Fallback)
}

// languageFromPath derives a chroma-friendly lexer name from a file
// extension. Empty string when the extension isn't recognized — the
// caller can still fall back to content analysis.
func languageFromPath(path string) string {
	if path == "" {
		return ""
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	if ext == "" {
		// Some files are extensionless but well-known by basename.
		base := strings.ToLower(filepath.Base(path))
		switch base {
		case "dockerfile", "containerfile":
			return "docker"
		case "makefile", "gnumakefile":
			return "make"
		}
		return ""
	}
	// Direct extension → chroma lexer name.
	switch ext {
	case "go":
		return "go"
	case "py":
		return "python"
	case "rs":
		return "rust"
	case "js", "mjs", "cjs":
		return "javascript"
	case "ts":
		return "typescript"
	case "tsx":
		return "tsx"
	case "jsx":
		return "jsx"
	case "rb":
		return "ruby"
	case "java":
		return "java"
	case "c", "h":
		return "c"
	case "cc", "cpp", "cxx", "hpp", "hxx":
		return "cpp"
	case "cs":
		return "csharp"
	case "kt", "kts":
		return "kotlin"
	case "swift":
		return "swift"
	case "sh", "bash", "zsh":
		return "bash"
	case "yaml", "yml":
		return "yaml"
	case "json":
		return "json"
	case "toml":
		return "toml"
	case "xml":
		return "xml"
	case "html", "htm":
		return "html"
	case "css":
		return "css"
	case "md", "markdown":
		return "markdown"
	case "sql":
		return "sql"
	case "proto":
		return "protobuf"
	case "tf", "hcl":
		return "terraform"
	case "lua":
		return "lua"
	case "php":
		return "php"
	case "ex", "exs":
		return "elixir"
	case "erl":
		return "erlang"
	case "scala":
		return "scala"
	case "ml", "mli":
		return "ocaml"
	case "hs":
		return "haskell"
	case "r":
		return "r"
	case "dart":
		return "dart"
	case "vim":
		return "vim"
	case "diff", "patch":
		return "diff"
	}
	// Unknown extension — return it anyway; chroma may have a lexer
	// keyed on this name even if we don't enumerate it above.
	return ext
}
