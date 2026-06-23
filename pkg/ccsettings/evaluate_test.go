package ccsettings

import "testing"

// engineFromJSON builds an Engine over a single-scope (project) settings doc.
func engineFromJSON(t *testing.T, doc string, opts EngineOptions) *Engine {
	t.Helper()
	m := mergedFromJSON(t, map[Scope]string{ScopeProject: doc})
	return NewEngine(m, opts)
}

func TestEvaluateDenyShadowsAllow(t *testing.T) {
	// Bash(aws *) deny shadows the narrower Bash(aws s3 ls) allow.
	e := engineFromJSON(t, `{"permissions":{
		"allow":["Bash(aws s3 ls)"],
		"deny":["Bash(aws *)"]
	}}`, EngineOptions{})

	d := e.Evaluate(ToolCall{Tool: "Bash", Command: "aws s3 ls"})
	if d.Result != ResultDeny {
		t.Fatalf("result = %v, want deny", d.Result)
	}
	if d.MatchedRule != "Bash(aws *)" {
		t.Errorf("matched rule = %q, want Bash(aws *)", d.MatchedRule)
	}
}

func TestEvaluateBareBashDenyRemovesFromContext(t *testing.T) {
	e := engineFromJSON(t, `{"permissions":{"deny":["Bash"]}}`, EngineOptions{})
	d := e.Evaluate(ToolCall{Tool: "Bash", Command: "echo hi"})
	if d.Result != ResultDeny || !d.RemovedFromContext {
		t.Errorf("got %+v, want deny + RemovedFromContext", d)
	}

	// Bash(rm *) only blocks matching calls; the tool stays in context.
	e2 := engineFromJSON(t, `{"permissions":{"deny":["Bash(rm *)"]}}`, EngineOptions{})
	d2 := e2.Evaluate(ToolCall{Tool: "Bash", Command: "rm -rf build"})
	if d2.Result != ResultDeny || d2.RemovedFromContext {
		t.Errorf("got %+v, want deny WITHOUT RemovedFromContext", d2)
	}
}

func TestEvaluateToolNameGlobDeny(t *testing.T) {
	e := engineFromJSON(t, `{"permissions":{"deny":["mcp__*"]}}`, EngineOptions{})
	d := e.Evaluate(ToolCall{Tool: "mcp__github__create_issue"})
	if d.Result != ResultDeny || !d.RemovedFromContext {
		t.Errorf("got %+v, want deny + RemovedFromContext for mcp__* glob", d)
	}
}

func TestEvaluateUnanchoredAllowGlobSkipped(t *testing.T) {
	// An unanchored allow glob like "*" must not auto-approve anything.
	e := engineFromJSON(t, `{"permissions":{"allow":["*"]}}`, EngineOptions{})
	d := e.Evaluate(ToolCall{Tool: "Bash", Command: "echo hi"})
	if d.Result == ResultAllow {
		t.Errorf("unanchored allow glob should not allow, got %+v", d)
	}
}

func TestEvaluateCompoundAllowRequiresEverySub(t *testing.T) {
	e := engineFromJSON(t, `{"permissions":{"allow":["Bash(npm test *)"]}}`, EngineOptions{})
	// git status is not covered by any allow rule → not allowed.
	d := e.Evaluate(ToolCall{Tool: "Bash", Command: "git status && npm test"})
	if d.Result == ResultAllow {
		t.Errorf("compound with uncovered subcommand should not be allowed, got %+v", d)
	}

	e2 := engineFromJSON(t, `{"permissions":{"allow":["Bash(git status)","Bash(npm test *)"]}}`, EngineOptions{})
	d2 := e2.Evaluate(ToolCall{Tool: "Bash", Command: "git status && npm test"})
	if d2.Result != ResultAllow {
		t.Errorf("fully-covered compound should be allowed, got %+v", d2)
	}
}

func TestEvaluateCompoundDenyAnySub(t *testing.T) {
	e := engineFromJSON(t, `{"permissions":{"deny":["Bash(git push *)"]}}`, EngineOptions{})
	d := e.Evaluate(ToolCall{Tool: "Bash", Command: "npm test && git push origin main"})
	if d.Result != ResultDeny {
		t.Errorf("compound with a denied subcommand should be denied, got %+v", d)
	}
}

func TestEvaluateWrapperStripped(t *testing.T) {
	e := engineFromJSON(t, `{"permissions":{"allow":["Bash(npm test *)"]}}`, EngineOptions{})
	d := e.Evaluate(ToolCall{Tool: "Bash", Command: "timeout 30 npm test"})
	if d.Result != ResultAllow {
		t.Errorf("timeout-wrapped command should match allow, got %+v", d)
	}
}

func TestEvaluateReadDotEnvEquivalence(t *testing.T) {
	for _, rule := range []string{"Read(.env)", "Read(**/.env)"} {
		e := engineFromJSON(t, `{"permissions":{"deny":["`+rule+`"]}}`, EngineOptions{})
		d := e.Evaluate(ToolCall{Tool: "Read", Path: "/work/proj/config/.env"})
		if d.Result != ResultDeny {
			t.Errorf("%s should deny nested .env, got %+v", rule, d)
		}
		// A .env in a parent directory is NOT blocked.
		d2 := e.Evaluate(ToolCall{Tool: "Read", Path: "/work/.env"})
		if d2.Result == ResultDeny {
			t.Errorf("%s should not block parent-dir .env, got %+v", rule, d2)
		}
	}
}

func TestEvaluateSymlinkAsymmetry(t *testing.T) {
	// Read(./project/**) allowed, Read(~/.ssh/**) denied. A symlink under the
	// allowed dir that points into the denied dir is blocked.
	doc := `{"permissions":{
		"allow":["Read(./project/**)"],
		"deny":["Read(~/.ssh/**)"]
	}}`
	e := engineFromJSON(t, doc, EngineOptions{})

	blocked := e.Evaluate(ToolCall{
		Tool:          "Read",
		Path:          "/work/proj/project/key",
		SymlinkTarget: "/home/alice/.ssh/id_rsa",
	})
	if blocked.Result != ResultDeny {
		t.Errorf("symlink into denied dir should be denied, got %+v", blocked)
	}

	// Allow alone (no deny): a symlink pointing outside the allowed dir does
	// NOT auto-allow — it falls back to prompting.
	allowOnly := engineFromJSON(t, `{"permissions":{"allow":["Read(./project/**)"]}}`, EngineOptions{})
	d := allowOnly.Evaluate(ToolCall{
		Tool:          "Read",
		Path:          "/work/proj/project/key",
		SymlinkTarget: "/home/alice/.ssh/id_rsa",
	})
	if d.Result == ResultAllow {
		t.Errorf("allow requires BOTH symlink and target to match; got %+v", d)
	}
	// A symlink whose target is also inside the allowed dir IS allowed.
	in := allowOnly.Evaluate(ToolCall{
		Tool:          "Read",
		Path:          "/work/proj/project/link",
		SymlinkTarget: "/work/proj/project/real.txt",
	})
	if in.Result != ResultAllow {
		t.Errorf("symlink with both paths allowed should be allowed, got %+v", in)
	}
}

func TestEvaluateWebFetchDomains(t *testing.T) {
	e := engineFromJSON(t, `{"permissions":{
		"allow":["WebFetch(domain:*.example.com)"],
		"deny":["WebFetch(domain:evil.example.com)"]
	}}`, EngineOptions{})

	if d := e.Evaluate(ToolCall{Tool: "WebFetch", URL: "https://api.example.com/x"}); d.Result != ResultAllow {
		t.Errorf("api.example.com should be allowed, got %+v", d)
	}
	if d := e.Evaluate(ToolCall{Tool: "WebFetch", URL: "https://evil.example.com/x"}); d.Result != ResultDeny {
		t.Errorf("evil.example.com should be denied, got %+v", d)
	}
	if d := e.Evaluate(ToolCall{Tool: "WebFetch", URL: "https://example.com/x"}); d.Result != ResultPrompt {
		t.Errorf("apex example.com should fall through to prompt, got %+v", d)
	}
}

func TestEvaluateParamRules(t *testing.T) {
	e := engineFromJSON(t, `{"permissions":{"deny":["Agent(model:opus)","Bash(run_in_background:true)"]}}`, EngineOptions{})

	if d := e.Evaluate(ToolCall{Tool: "Agent", Params: map[string]any{"model": "opus"}}); d.Result != ResultDeny {
		t.Errorf("Agent(model:opus) should deny, got %+v", d)
	}
	// Omitted parameter is never matched.
	if d := e.Evaluate(ToolCall{Tool: "Agent", Params: map[string]any{}}); d.Result == ResultDeny {
		t.Errorf("omitted model should not match Agent(model:opus), got %+v", d)
	}
	// Bash param rule.
	if d := e.Evaluate(ToolCall{Tool: "Bash", Command: "sleep 100", Params: map[string]any{"run_in_background": true}}); d.Result != ResultDeny {
		t.Errorf("Bash(run_in_background:true) should deny, got %+v", d)
	}
}

func TestEvaluateAskBeatsAllow(t *testing.T) {
	e := engineFromJSON(t, `{"permissions":{
		"allow":["Bash(git push *)"],
		"ask":["Bash(git push *)"]
	}}`, EngineOptions{})
	d := e.Evaluate(ToolCall{Tool: "Bash", Command: "git push origin main"})
	if d.Result != ResultAsk {
		t.Errorf("ask should take precedence over allow, got %+v", d)
	}
}

func TestEvaluateSandboxAutoAllowSkipsBareBashAsk(t *testing.T) {
	doc := `{"permissions":{"ask":["Bash","Bash(git push *)"]},"sandbox":{"excludedCommands":["docker *"]}}`
	m := mergedFromJSON(t, map[Scope]string{ScopeProject: doc})
	e := NewEngine(m, EngineOptions{SandboxAutoAllowBash: true})

	// A sandboxed command: the bare Bash ask is skipped.
	if d := e.Evaluate(ToolCall{Tool: "Bash", Command: "echo hi"}); d.Result == ResultAsk {
		t.Errorf("bare Bash ask should be skipped for sandboxed command, got %+v", d)
	}
	// A content-scoped ask still forces a prompt.
	if d := e.Evaluate(ToolCall{Tool: "Bash", Command: "git push origin main"}); d.Result != ResultAsk {
		t.Errorf("content-scoped ask should still prompt, got %+v", d)
	}
	// An excluded command falls back to the regular bare Bash ask.
	if d := e.Evaluate(ToolCall{Tool: "Bash", Command: "docker ps"}); d.Result != ResultAsk {
		t.Errorf("excluded command should respect bare Bash ask, got %+v", d)
	}
}

func TestEvaluateCrossScopeDenyWins(t *testing.T) {
	// A user-level deny blocks a project-level allow (deny-first across scopes).
	m := mergedFromJSON(t, map[Scope]string{
		ScopeUser:    `{"permissions":{"deny":["Bash(rm *)"]}}`,
		ScopeProject: `{"permissions":{"allow":["Bash(rm *)"]}}`,
	})
	e := NewEngine(m, EngineOptions{})
	d := e.Evaluate(ToolCall{Tool: "Bash", Command: "rm -rf build"})
	if d.Result != ResultDeny || d.SourceScope != ScopeUser {
		t.Errorf("user deny should beat project allow, got %+v", d)
	}
}
