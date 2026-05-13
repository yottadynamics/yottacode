package usercmd

import (
	"regexp"
	"strings"
)

// argRe matches $ARGUMENTS or $1..$9 anywhere in the body. Stopping at
// single-digit positionals matches Claude Code's documented behavior;
// the consequence is that "$10" expands as "<value of $1>0" rather
// than a "tenth argument" placeholder. This is documented; v2 can add
// ${N} for double-digit indexing if a real use case shows up.
var argRe = regexp.MustCompile(`\$ARGUMENTS|\$[1-9]`)

// Substitute resolves $ARGUMENTS and $1..$9 in body using args. args
// is the post-name remainder a user typed after the command — for
// "/foo a b c", args is "a b c".
//
// Tokenization is strings.Fields (whitespace-split, collapsing runs)
// to match what runSlash already does on the command line. Missing
// positionals (e.g. $3 when only two args were supplied) resolve to
// the empty string; the command author is responsible for handling
// the empty case in their prose if it matters.
func Substitute(body, args string) string {
	tokens := strings.Fields(args)
	return argRe.ReplaceAllStringFunc(body, func(match string) string {
		if match == "$ARGUMENTS" {
			return args
		}
		// $1..$9 → tokens[match[1]-'1']
		idx := int(match[1] - '0')
		if idx < 1 || idx > len(tokens) {
			return ""
		}
		return tokens[idx-1]
	})
}
