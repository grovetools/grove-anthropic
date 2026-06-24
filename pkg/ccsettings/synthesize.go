package ccsettings

import "strings"

// This file is the inverse of the rule parser (rule.go / bash.go): given a
// concrete Bash command an agent actually ran, it synthesizes a *ladder* of
// candidate permission rules of increasing generality, mirroring Claude's own
// "Yes, don't ask again" rule synthesis. The TUI shows the rungs and the user
// picks one — we never auto-widen silently.
//
// The ladder for a single command is built by:
//   1. stripping recognized process wrappers (timeout/nice/…) via StripWrappers
//      so the rule matches the inner command;
//   2. emitting an exact rung (the whole command, verbatim);
//   3. emitting progressively shorter prefixes, each terminated by the
//      word-boundary " *" wildcard Claude uses, so `npm run test src/foo.ts`
//      yields `npm run test *`, `npm run *`, `npm *`.
//
// A compound command (`a && b`) has no single rule that covers it; the caller
// uses SplitCompound to produce one ladder per subcommand.

// RuleRung is one candidate permission rule at a chosen breadth, paired with a
// short human label describing how general it is.
type RuleRung struct {
	// Rule is the full permission rule string, e.g. "Bash(npm run test *)".
	Rule string
	// Specifier is the part inside the parentheses, e.g. "npm run test *".
	Specifier string
	// Label is a short breadth descriptor: "exact", "prefix", "family", "broad".
	Label string
	// Width is the number of leading command tokens the rung pins (the exact
	// rung pins every token; broader rungs pin fewer). Smaller = more general.
	Width int
}

// SynthesizeBashLadder produces the rule ladder for a single (already
// subcommand-level) Bash command, from most specific (exact) to most general.
// The command is wrapper-stripped first so the rules match the inner command.
// Duplicate rungs (which arise when the exact command is already a single
// token, or when trimming produces the same prefix) are collapsed. tool is the
// shell tool name to wrap the specifier in ("Bash" or "PowerShell").
func SynthesizeBashLadder(tool, command string) []RuleRung {
	inner := strings.TrimSpace(StripRedirections(StripWrappers(strings.TrimSpace(command))))
	if inner == "" {
		return nil
	}
	// A command containing a shell expansion cannot be allow-listed by a content
	// rule (Claude prompts regardless), so offer no rungs — the caller surfaces a
	// note via SynthesizeLadders instead.
	if ContainsShellExpansion(inner) {
		return nil
	}
	tokens := shellFields(inner)
	if len(tokens) == 0 {
		return nil
	}

	var rungs []RuleRung
	seen := map[string]struct{}{}
	add := func(spec, label string, width int) {
		if spec == "" {
			return
		}
		if _, dup := seen[spec]; dup {
			return
		}
		seen[spec] = struct{}{}
		rungs = append(rungs, RuleRung{
			Rule:      tool + "(" + spec + ")",
			Specifier: spec,
			Label:     label,
			Width:     width,
		})
	}

	// Exact rung: the whole command verbatim. Width is len+1 so the verbatim
	// rung always sorts ahead of the all-tokens "+ args" rung below (which pins
	// the same token count).
	add(inner, "exact", len(tokens)+1)

	// Prefix rungs: pin the first k tokens (k from len down to 1) and append the
	// word-boundary wildcard. Starting at k=len yields the useful "this command
	// with any arguments" rung even for a command that took none, e.g.
	// `git status` → `git status *`; `npm run test src/foo.ts` →
	//   k=3: npm run test *   (prefix)
	//   k=2: npm run *        (family)
	//   k=1: npm *            (broad)
	for k := len(tokens); k >= 1; k-- {
		spec := strings.Join(tokens[:k], " ") + " *"
		add(spec, ladderLabel(k, len(tokens)), k)
	}

	return rungs
}

// ladderLabel names a prefix rung by how many tokens it pins relative to the
// full command: the broadest (single-token) rung is "broad", the next "family",
// and any tighter prefix "prefix".
func ladderLabel(width, total int) string {
	switch {
	case width <= 1:
		return "broad"
	case width == 2:
		return "family"
	default:
		return "prefix"
	}
}

// CommandLadder bundles a subcommand with the ladder synthesized for it. A
// compound command yields one CommandLadder per subcommand.
type CommandLadder struct {
	// Subcommand is the wrapper/redirection-stripped subcommand the ladder
	// generalizes.
	Subcommand string
	Rungs      []RuleRung
	// Note is set (with Rungs nil) when the subcommand cannot be allow-listed —
	// currently when it contains a shell expansion — to explain why no rungs are
	// offered.
	Note string
}

// SynthesizeLadders splits a (possibly compound) Bash command into its
// subcommands and returns one ladder per subcommand, in command order. Each
// subcommand is wrapper-stripped before laddering, matching how the evaluator
// matches it. Subcommands that produce no rungs (empty after stripping) are
// dropped.
func SynthesizeLadders(tool, command string) []CommandLadder {
	subs := SplitCompound(command)
	out := make([]CommandLadder, 0, len(subs))
	for _, sub := range subs {
		stripped := strings.TrimSpace(StripRedirections(StripWrappers(strings.TrimSpace(sub))))
		if stripped == "" {
			continue
		}
		if ContainsShellExpansion(stripped) {
			out = append(out, CommandLadder{
				Subcommand: stripped,
				Note:       "contains a shell expansion ($…/`…`) — Claude prompts regardless of any allow rule; approve interactively or rewrite without the expansion",
			})
			continue
		}
		rungs := SynthesizeBashLadder(tool, stripped)
		if len(rungs) == 0 {
			continue
		}
		out = append(out, CommandLadder{Subcommand: stripped, Rungs: rungs})
	}
	return out
}
