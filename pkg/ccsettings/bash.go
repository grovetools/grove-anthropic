package ccsettings

import (
	"regexp"
	"strings"
	"sync"
)

// compoundSeparators are the shell operators Claude recognizes when splitting a
// compound command into independently-matched subcommands: && || ; | |& & and
// newlines. Order matters — multi-character operators are tried before their
// single-character prefixes.
var compoundSeparators = []string{"&&", "||", "|&", "&", "|", ";", "\n", "\r"}

// SplitCompound splits a Bash command string into its subcommands on shell
// operators. It is intentionally naive about quoting (matching Claude's own
// "match each subcommand independently" rule). Empty fragments are dropped.
//
// The lone `&` separator (background) is split with redirection awareness so a
// file-descriptor redirection like `2>&1`, `>&2`, or `&>file` is NOT torn apart
// into a phantom subcommand — splitting `git status 2>&1` on a naive `&` would
// otherwise yield the bogus subcommands `git status 2>` and `1`.
func SplitCompound(cmd string) []string {
	parts := []string{cmd}
	for _, sep := range compoundSeparators {
		var next []string
		for _, p := range parts {
			if sep == "&" {
				next = append(next, splitBackgroundAmp(p)...)
			} else {
				next = append(next, strings.Split(p, sep)...)
			}
		}
		parts = next
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// splitBackgroundAmp splits s on a background/separator `&`, but never on an `&`
// that belongs to a redirection (`>&`, `<&`, or `&>`), so redirections stay
// attached to their command.
func splitBackgroundAmp(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '&' {
			continue
		}
		var prev, nextc byte = ' ', ' '
		if i > 0 {
			prev = s[i-1]
		}
		if i+1 < len(s) {
			nextc = s[i+1]
		}
		if prev == '>' || prev == '<' || nextc == '>' {
			continue // part of a redirection (2>&1, >&2, &>file)
		}
		out = append(out, s[start:i])
		start = i + 1
	}
	return append(out, s[start:])
}

// redirToken matches a leading shell redirection operator on a token, optionally
// with an attached fd-dup (`&1`) or attached target (`2>/dev/null`).
var redirToken = regexp.MustCompile(`^([0-9]*(?:>>|<<<|<<|>|<)&?[0-9]*|&>>?)`)

// StripRedirections removes shell redirection operators and their targets from a
// command so synthesized rules ignore IO plumbing: `git status 2>&1` becomes
// `git status`, and `cmd 2>/dev/null > out` becomes `cmd`. It is quote-naive,
// which is fine because redirection operators are not normally quoted; a `>`
// inside a quoted argument is not at a token boundary and is left untouched.
func StripRedirections(cmd string) string {
	tokens := strings.Fields(cmd)
	out := make([]string, 0, len(tokens))
	for i := 0; i < len(tokens); i++ {
		op := redirToken.FindString(tokens[i])
		if op == "" {
			out = append(out, tokens[i])
			continue
		}
		rest := tokens[i][len(op):]
		// A bare operator (no attached target, no fd-dup) consumes the next
		// token as its target: `>` `file`, `2>` `/dev/null`.
		if rest == "" && !strings.Contains(op, "&") && i+1 < len(tokens) {
			i++
		}
	}
	return strings.Join(out, " ")
}

// shellExpansion matches an unexpanded shell expansion: $VAR, ${...}, $(...),
// $?/$@/$1/etc., or a backtick command substitution.
var shellExpansion = regexp.MustCompile("`" + `|\$[\w{(*?!@#-]`)

// ContainsShellExpansion reports whether a command contains a shell variable or
// command expansion. Such commands cannot be auto-approved by a content-scoped
// allow rule — Claude Code prompts regardless ("Contains simple_expansion"),
// because the expanded text is not knowable from the literal pattern.
func ContainsShellExpansion(cmd string) bool {
	return shellExpansion.MatchString(cmd)
}

// shellFields splits a command into whitespace-separated tokens but keeps a
// single- or double-quoted span (including its quotes) as one token, so prefix
// rungs never cut inside a quoted argument.
func shellFields(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case quote != 0:
			cur.WriteRune(r)
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
			cur.WriteRune(r)
		case r == ' ' || r == '\t':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// processWrappers are stripped from the front of a subcommand before matching,
// so a rule like Bash(npm test *) also matches `timeout 30 npm test`.
var processWrappers = map[string]struct{}{
	"timeout": {},
	"time":    {},
	"nice":    {},
	"nohup":   {},
	"stdbuf":  {},
}

// StripWrappers removes recognized leading process wrappers (timeout, time,
// nice, nohup, stdbuf) along with their option flags and value arguments, and
// strips a bare `xargs` (only when it carries no flags). The result is the
// inner command that permission rules are matched against.
func StripWrappers(cmd string) string {
	for {
		tokens := strings.Fields(cmd)
		if len(tokens) == 0 {
			return cmd
		}
		w := tokens[0]
		if w == "xargs" {
			// Only a flag-free `xargs` is stripped; `xargs -n1 grep` is
			// matched as an xargs command itself.
			if len(tokens) > 1 && !strings.HasPrefix(tokens[1], "-") {
				cmd = strings.Join(tokens[1:], " ")
				continue
			}
			return cmd
		}
		if _, ok := processWrappers[w]; !ok {
			return cmd
		}
		rest := tokens[1:]
		i := 0
		// Skip option flags belonging to the wrapper.
		for i < len(rest) && strings.HasPrefix(rest[i], "-") {
			flag := rest[i]
			i++
			// Flags that take a separate value argument.
			if needsFlagValue(w, flag) && i < len(rest) {
				i++
			}
		}
		// timeout and nice take a positional argument (duration / niceness)
		// before the command when it was not supplied via a flag.
		if (w == "timeout") && i < len(rest) {
			i++
		}
		if i >= len(rest) {
			return cmd // nothing left to unwrap; leave as-is
		}
		cmd = strings.Join(rest[i:], " ")
	}
}

// needsFlagValue reports whether a wrapper flag consumes the following token as
// its value (e.g. `timeout -s SIGKILL`, `nice -n 10`, `stdbuf -o L`).
func needsFlagValue(wrapper, flag string) bool {
	// Combined short flags like -n10 or -oL carry their own value.
	if len(flag) > 2 && flag[0] == '-' && flag[1] != '-' {
		return false
	}
	switch wrapper {
	case "timeout":
		return flag == "-s" || flag == "-k" || flag == "--signal" || flag == "--kill-after"
	case "nice":
		return flag == "-n" || flag == "--adjustment"
	case "stdbuf":
		return flag == "-i" || flag == "-o" || flag == "-e" ||
			flag == "--input" || flag == "--output" || flag == "--error"
	default:
		return false
	}
}

// matchBashPattern reports whether a single Bash rule pattern matches a single
// (already wrapper-stripped) subcommand. It honors the trailing " *" / ":*"
// word boundary and treats `*` elsewhere as "any sequence including spaces".
func matchBashPattern(pattern, cmd string) bool {
	re := bashPatternRegexp(pattern)
	return re.MatchString(cmd)
}

var bashRegexpCache sync.Map // string -> *regexp.Regexp

func bashPatternRegexp(pattern string) *regexp.Regexp {
	if v, ok := bashRegexpCache.Load(pattern); ok {
		return v.(*regexp.Regexp)
	}
	re := regexp.MustCompile(buildBashRegexp(pattern))
	bashRegexpCache.Store(pattern, re)
	return re
}

// buildBashRegexp translates a Bash permission pattern into an anchored regexp.
//
//   - A trailing " *" (space then star) or ":*" enforces a word boundary: the
//     prefix must be followed by a space or the end of string. So `ls *`
//     matches `ls` and `ls -la` but not `lsof`.
//   - A `*` anywhere else matches any sequence of characters, including spaces,
//     so one wildcard can span multiple arguments.
func buildBashRegexp(pattern string) string {
	// Normalize a trailing ":*" to the equivalent trailing " *".
	if strings.HasSuffix(pattern, ":*") {
		pattern = pattern[:len(pattern)-2] + " *"
	}
	var b strings.Builder
	b.WriteString("^")
	if strings.HasSuffix(pattern, " *") {
		prefix := pattern[:len(pattern)-2]
		b.WriteString(translateBashGlob(prefix))
		// Word boundary: end-of-string OR a space followed by anything.
		b.WriteString(`(?:\s.*)?`)
	} else {
		b.WriteString(translateBashGlob(pattern))
	}
	b.WriteString("$")
	return b.String()
}

// translateBashGlob converts the literal/`*` parts of a pattern into a regexp
// fragment, escaping everything that is not a `*` and mapping `*` to `.*`.
func translateBashGlob(pattern string) string {
	var b strings.Builder
	for _, c := range pattern {
		if c == '*' {
			b.WriteString(".*")
			continue
		}
		b.WriteString(regexp.QuoteMeta(string(c)))
	}
	return b.String()
}
