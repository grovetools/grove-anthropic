package ccsettings

import "encoding/json"

// Settings is the typed view of a single Claude Code settings.json file. Keys
// the model recognizes are decoded into fields; everything else is preserved
// in Unknown so newer Claude versions never get config silently dropped.
//
// The struct intentionally covers the keys the settings browser renders richly
// (permissions, sandbox, MCP, env, hooks) plus the long tail of scalar toggles
// from the settings reference. The long-tail toggles are typed only loosely
// (pointers, so "unset" is distinguishable) since the engine does not interpret
// most of them — they exist so the model can display and round-trip them.
type Settings struct {
	Schema string `json:"$schema,omitempty"`

	// Permissions and sandbox are the semantically rich subtrees the
	// evaluation engine consumes.
	Permissions *Permissions `json:"permissions,omitempty"`
	Sandbox     *Sandbox     `json:"sandbox,omitempty"`

	// Hooks is kept raw: its shape is complex and the engine does not
	// interpret it, but it must round-trip.
	Hooks json.RawMessage `json:"hooks,omitempty"`

	// Env is the environment-variable map applied to all sessions.
	Env map[string]string `json:"env,omitempty"`

	// MCP server controls.
	EnableAllProjectMcpServers *bool           `json:"enableAllProjectMcpServers,omitempty"`
	EnabledMcpjsonServers      []string        `json:"enabledMcpjsonServers,omitempty"`
	DisabledMcpjsonServers     []string        `json:"disabledMcpjsonServers,omitempty"`
	AllowedMcpServers          json.RawMessage `json:"allowedMcpServers,omitempty"`          // managed only
	DeniedMcpServers           json.RawMessage `json:"deniedMcpServers,omitempty"`           // merges from all scopes
	AllowManagedMcpServersOnly *bool           `json:"allowManagedMcpServersOnly,omitempty"` // managed only

	// Model / behavior toggles (long tail). Pointers preserve unset vs false.
	Model               string          `json:"model,omitempty"`
	EffortLevel         string          `json:"effortLevel,omitempty"`
	OutputStyle         string          `json:"outputStyle,omitempty"`
	Language            string          `json:"language,omitempty"`
	APIKeyHelper        string          `json:"apiKeyHelper,omitempty"`
	ForceLoginMethod    string          `json:"forceLoginMethod,omitempty"`
	CleanupPeriodDays   *int            `json:"cleanupPeriodDays,omitempty"`
	IncludeCoAuthoredBy *bool           `json:"includeCoAuthoredBy,omitempty"`
	AlwaysThinking      *bool           `json:"alwaysThinkingEnabled,omitempty"`
	AutoCompactEnabled  *bool           `json:"autoCompactEnabled,omitempty"`
	DisableAllHooks     *bool           `json:"disableAllHooks,omitempty"`
	StatusLine          json.RawMessage `json:"statusLine,omitempty"`
	Attribution         json.RawMessage `json:"attribution,omitempty"`

	// SkipDangerousModePermissionPrompt records whether the user has accepted
	// the bypass-permissions-mode dialog. It is a top-level key (not nested
	// under "permissions") but it is permission-adjacent — it gates the
	// dangerous bypass mode — so the model types it explicitly to render it in
	// the Permissions/Effective views with provenance rather than as an opaque
	// passthrough key.
	SkipDangerousModePermissionPrompt *bool `json:"skipDangerousModePermissionPrompt,omitempty"`

	// Unknown holds keys not recognized by the typed model above, preserved
	// verbatim for forward compatibility and round-trip on write.
	Unknown map[string]json.RawMessage `json:"-"`

	// DecodeWarnings records known keys whose value did not match the typed
	// shape (a drift signal); their raw value is preserved in Unknown.
	DecodeWarnings []string `json:"-"`
}

// UnmarshalJSON decodes known fields tolerantly and captures everything else in
// Unknown. See decodeKnown for the per-key error isolation contract.
func (s *Settings) UnmarshalJSON(data []byte) error {
	type alias Settings
	var a alias
	unknown, warnings, err := decodeKnown(data, &a)
	if err != nil {
		return err
	}
	*s = Settings(a)
	s.Unknown = unknown
	s.DecodeWarnings = warnings
	return nil
}

// MarshalJSON re-emits typed fields and folds preserved unknown keys back in.
func (s Settings) MarshalJSON() ([]byte, error) {
	type alias Settings
	return marshalWithUnknown(alias(s), s.Unknown)
}

// Permissions mirrors the "permissions" object. allow/ask/deny are rule-string
// arrays; additionalDirectories grants file access; the *Only flags are managed
// lockdowns; the disable* flags are mode locks.
type Permissions struct {
	Allow                 []string `json:"allow,omitempty"`
	Ask                   []string `json:"ask,omitempty"`
	Deny                  []string `json:"deny,omitempty"`
	AdditionalDirectories []string `json:"additionalDirectories,omitempty"`
	DefaultMode           string   `json:"defaultMode,omitempty"`

	DisableBypassPermissionsMode    string `json:"disableBypassPermissionsMode,omitempty"` // "disable"
	DisableAutoMode                 string `json:"disableAutoMode,omitempty"`              // "disable"
	AllowManagedPermissionRulesOnly *bool  `json:"allowManagedPermissionRulesOnly,omitempty"`

	Unknown map[string]json.RawMessage `json:"-"`
}

func (p *Permissions) UnmarshalJSON(data []byte) error {
	type alias Permissions
	var a alias
	unknown, _, err := decodeKnown(data, &a)
	if err != nil {
		return err
	}
	*p = Permissions(a)
	p.Unknown = unknown
	return nil
}

func (p Permissions) MarshalJSON() ([]byte, error) {
	type alias Permissions
	return marshalWithUnknown(alias(p), p.Unknown)
}

// Sandbox mirrors the "sandbox" object.
type Sandbox struct {
	Enabled                  *bool    `json:"enabled,omitempty"`
	FailIfUnavailable        *bool    `json:"failIfUnavailable,omitempty"`
	AllowUnsandboxedCommands *bool    `json:"allowUnsandboxedCommands,omitempty"`
	AutoAllowBashIfSandboxed *bool    `json:"autoAllowBashIfSandboxed,omitempty"`
	ExcludedCommands         []string `json:"excludedCommands,omitempty"`

	Filesystem *SandboxFilesystem `json:"filesystem,omitempty"`
	Network    *SandboxNetwork    `json:"network,omitempty"`

	AllowAppleEvents             *bool `json:"allowAppleEvents,omitempty"`
	EnableWeakerNetworkIsolation *bool `json:"enableWeakerNetworkIsolation,omitempty"`
	EnableWeakerNestedSandbox    *bool `json:"enableWeakerNestedSandbox,omitempty"`

	Unknown map[string]json.RawMessage `json:"-"`
}

func (s *Sandbox) UnmarshalJSON(data []byte) error {
	type alias Sandbox
	var a alias
	unknown, _, err := decodeKnown(data, &a)
	if err != nil {
		return err
	}
	*s = Sandbox(a)
	s.Unknown = unknown
	return nil
}

func (s Sandbox) MarshalJSON() ([]byte, error) {
	type alias Sandbox
	return marshalWithUnknown(alias(s), s.Unknown)
}

// SandboxFilesystem mirrors "sandbox.filesystem".
type SandboxFilesystem struct {
	AllowWrite                []string `json:"allowWrite,omitempty"`
	DenyWrite                 []string `json:"denyWrite,omitempty"`
	DenyRead                  []string `json:"denyRead,omitempty"`
	AllowRead                 []string `json:"allowRead,omitempty"`
	AllowManagedReadPathsOnly *bool    `json:"allowManagedReadPathsOnly,omitempty"` // managed only

	Unknown map[string]json.RawMessage `json:"-"`
}

func (f *SandboxFilesystem) UnmarshalJSON(data []byte) error {
	type alias SandboxFilesystem
	var a alias
	unknown, _, err := decodeKnown(data, &a)
	if err != nil {
		return err
	}
	*f = SandboxFilesystem(a)
	f.Unknown = unknown
	return nil
}

func (f SandboxFilesystem) MarshalJSON() ([]byte, error) {
	type alias SandboxFilesystem
	return marshalWithUnknown(alias(f), f.Unknown)
}

// SandboxNetwork mirrors "sandbox.network".
type SandboxNetwork struct {
	AllowedDomains          []string `json:"allowedDomains,omitempty"`
	DeniedDomains           []string `json:"deniedDomains,omitempty"`
	AllowManagedDomainsOnly *bool    `json:"allowManagedDomainsOnly,omitempty"` // managed only
	AllowUnixSockets        []string `json:"allowUnixSockets,omitempty"`
	AllowAllUnixSockets     *bool    `json:"allowAllUnixSockets,omitempty"`
	AllowLocalBinding       *bool    `json:"allowLocalBinding,omitempty"`
	HTTPProxyPort           *int     `json:"httpProxyPort,omitempty"`
	SOCKSProxyPort          *int     `json:"socksProxyPort,omitempty"`

	Unknown map[string]json.RawMessage `json:"-"`
}

func (n *SandboxNetwork) UnmarshalJSON(data []byte) error {
	type alias SandboxNetwork
	var a alias
	unknown, _, err := decodeKnown(data, &a)
	if err != nil {
		return err
	}
	*n = SandboxNetwork(a)
	n.Unknown = unknown
	return nil
}

func (n SandboxNetwork) MarshalJSON() ([]byte, error) {
	type alias SandboxNetwork
	return marshalWithUnknown(alias(n), n.Unknown)
}
