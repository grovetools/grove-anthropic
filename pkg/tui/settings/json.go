package settings

import (
	"encoding/json"
	"io"

	"github.com/grovetools/grove-anthropic/pkg/ccsettings"
)

// PrintJSON writes the merged settings plus per-element provenance to w,
// mirroring `grove config --json`: a headless, TTY-free view of the resolved
// configuration. Every array element and scalar carries the scope that produced
// it, and the computed filesystem/network boundaries are included.
func PrintJSON(d *Data, w io.Writer) error {
	out := jsonOutput{
		Context: jsonContext{
			CWD:         d.Ctx.CWD,
			ProjectRoot: d.Ctx.ProjectRoot,
			HomeDir:     d.Ctx.HomeDir,
		},
		Sources:     toJSONSources(d.Sources),
		Permissions: toJSONPermissions(d.Merged),
		Sandbox:     toJSONSandbox(d),
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

type jsonOutput struct {
	Context     jsonContext     `json:"context"`
	Sources     []jsonScope     `json:"sources"`
	Permissions jsonPermissions `json:"permissions"`
	Sandbox     jsonSandbox     `json:"sandbox"`
}

type jsonContext struct {
	CWD         string `json:"cwd"`
	ProjectRoot string `json:"projectRoot"`
	HomeDir     string `json:"homeDir"`
}

type jsonScope struct {
	Scope      string `json:"scope"`
	Path       string `json:"path"`
	Exists     bool   `json:"exists"`
	Managed    bool   `json:"managed"`
	ParseError string `json:"parseError,omitempty"`
	Unknown    int    `json:"unknownKeys,omitempty"`
}

type jsonRule struct {
	Rule  string `json:"rule"`
	Scope string `json:"scope"`
}

type jsonString struct {
	Value string `json:"value"`
	Scope string `json:"scope"`
}

type jsonProvBool struct {
	Set   bool   `json:"set"`
	Value bool   `json:"value"`
	Scope string `json:"scope,omitempty"`
}

type jsonScalar struct {
	Value string `json:"value"`
	Scope string `json:"scope,omitempty"`
}

type jsonBoundary struct {
	Path   string `json:"path"`
	Raw    string `json:"raw"`
	Scope  string `json:"scope"`
	Source string `json:"source"`
}

type jsonPermissions struct {
	Allow                           []jsonRule   `json:"allow"`
	Ask                             []jsonRule   `json:"ask"`
	Deny                            []jsonRule   `json:"deny"`
	AdditionalDirectories           []jsonString `json:"additionalDirectories"`
	DefaultMode                     jsonScalar   `json:"defaultMode"`
	DisableBypassPermissionsMode    jsonScalar   `json:"disableBypassPermissionsMode"`
	DisableAutoMode                 jsonScalar   `json:"disableAutoMode"`
	AllowManagedPermissionRulesOnly bool         `json:"allowManagedPermissionRulesOnly"`
}

type jsonSandbox struct {
	Enabled                   jsonProvBool   `json:"enabled"`
	FailIfUnavailable         jsonProvBool   `json:"failIfUnavailable"`
	AllowUnsandboxedCommands  jsonProvBool   `json:"allowUnsandboxedCommands"`
	ExcludedCommands          []jsonString   `json:"excludedCommands"`
	AllowManagedReadPathsOnly bool           `json:"allowManagedReadPathsOnly"`
	AllowManagedDomainsOnly   bool           `json:"allowManagedDomainsOnly"`
	FilesystemBoundary        jsonFSBoundary `json:"filesystemBoundary"`
	NetworkPolicy             jsonNetPolicy  `json:"networkPolicy"`
}

type jsonFSBoundary struct {
	AllowWrite []jsonBoundary `json:"allowWrite"`
	DenyWrite  []jsonBoundary `json:"denyWrite"`
	AllowRead  []jsonBoundary `json:"allowRead"`
	DenyRead   []jsonBoundary `json:"denyRead"`
}

type jsonNetPolicy struct {
	AllowedDomains          []jsonBoundary `json:"allowedDomains"`
	DeniedDomains           []jsonBoundary `json:"deniedDomains"`
	AllowManagedDomainsOnly bool           `json:"allowManagedDomainsOnly"`
}

func toJSONSources(sources []ccsettings.SourceFile) []jsonScope {
	out := make([]jsonScope, 0, len(sources))
	for _, sf := range sources {
		js := jsonScope{
			Scope:   sf.Scope.Label(),
			Path:    sf.Path,
			Exists:  sf.Exists,
			Managed: sf.Scope == ccsettings.ScopeManaged,
		}
		if sf.ParseError != nil {
			js.ParseError = sf.ParseError.Error()
		}
		if sf.Settings != nil {
			js.Unknown = len(sf.Settings.Unknown)
		}
		out = append(out, js)
	}
	return out
}

func toJSONPermissions(m *ccsettings.MergedSettings) jsonPermissions {
	return jsonPermissions{
		Allow:                           toJSONRules(m.Allow),
		Ask:                             toJSONRules(m.Ask),
		Deny:                            toJSONRules(m.Deny),
		AdditionalDirectories:           toJSONStrings(m.AdditionalDirectories),
		DefaultMode:                     toJSONScalar(m.DefaultMode),
		DisableBypassPermissionsMode:    toJSONScalar(m.DisableBypassPermissionsMode),
		DisableAutoMode:                 toJSONScalar(m.DisableAutoMode),
		AllowManagedPermissionRulesOnly: m.AllowManagedPermissionRulesOnly,
	}
}

func toJSONSandbox(d *Data) jsonSandbox {
	m := d.Merged
	return jsonSandbox{
		Enabled:                   toJSONProvBool(m.SandboxEnabled),
		FailIfUnavailable:         toJSONProvBool(m.SandboxFailIfUnavailable),
		AllowUnsandboxedCommands:  toJSONProvBool(m.SandboxAllowUnsandboxedCommands),
		ExcludedCommands:          toJSONStrings(m.SandboxExcludedCommands),
		AllowManagedReadPathsOnly: m.AllowManagedReadPathsOnly,
		AllowManagedDomainsOnly:   m.AllowManagedDomainsOnly,
		FilesystemBoundary: jsonFSBoundary{
			AllowWrite: toJSONBoundaries(d.FS.AllowWrite),
			DenyWrite:  toJSONBoundaries(d.FS.DenyWrite),
			AllowRead:  toJSONBoundaries(d.FS.AllowRead),
			DenyRead:   toJSONBoundaries(d.FS.DenyRead),
		},
		NetworkPolicy: jsonNetPolicy{
			AllowedDomains:          toJSONBoundaries(d.Net.AllowedDomains),
			DeniedDomains:           toJSONBoundaries(d.Net.DeniedDomains),
			AllowManagedDomainsOnly: d.Net.AllowManagedDomainsOnly,
		},
	}
}

func toJSONRules(rules []ccsettings.ProvenancedRule) []jsonRule {
	out := make([]jsonRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, jsonRule{Rule: r.Rule, Scope: r.Scope.Label()})
	}
	return out
}

func toJSONStrings(vals []ccsettings.ProvenancedString) []jsonString {
	out := make([]jsonString, 0, len(vals))
	for _, v := range vals {
		out = append(out, jsonString{Value: v.Value, Scope: v.Scope.Label()})
	}
	return out
}

func toJSONScalar(s ccsettings.ProvenancedString) jsonScalar {
	js := jsonScalar{Value: s.Value}
	if s.Value != "" {
		js.Scope = s.Scope.Label()
	}
	return js
}

func toJSONProvBool(b ccsettings.ProvenancedBool) jsonProvBool {
	js := jsonProvBool{Set: b.Set, Value: b.Value}
	if b.Set {
		js.Scope = b.Scope.Label()
	}
	return js
}

func toJSONBoundaries(entries []ccsettings.BoundaryEntry) []jsonBoundary {
	out := make([]jsonBoundary, 0, len(entries))
	for _, e := range entries {
		out = append(out, jsonBoundary{
			Path:   e.Path,
			Raw:    e.Raw,
			Scope:  e.Scope.Label(),
			Source: e.Source,
		})
	}
	return out
}
