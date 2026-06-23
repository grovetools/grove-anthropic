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
func SplitCompound(cmd string) []string {
	parts := []string{cmd}
	for _, sep := range compoundSeparators {
		var next []string
		for _, p := range parts {
			next = append(next, strings.Split(p, sep)...)
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
