package ccsettings

import (
	_ "embed"
	"encoding/json"
	"reflect"
	"sort"
	"sync"

	"github.com/tailscale/hujson"
)

// schema.go folds a vendored snapshot of the community "Claude Code Settings"
// JSON Schema (SchemaStore) into the typed model so that settings keys our Go
// structs do not type are no longer opaque. The schema's top-level properties
// are parsed into a key -> {type, description, enum} index, and every key found
// in a real settings file is classified into one of three buckets:
//
//   - TYPED:            recognized by our Go structs (Settings field set).
//   - PASSTHROUGH_KNOWN: not typed, but present in the vendored schema, so we
//     can attach a type + description + enum from the schema.
//   - UNKNOWN:          in neither our model nor the schema — the true
//     forward-compat tail (a brand-new Anthropic key, or a typo).
//
// The schema is vendored (see schema/README.md) and embedded; it is never
// fetched at runtime. It is community-maintained and unversioned, so a drift
// test (schema_test.go) forces conscious classification when it is refreshed.

//go:embed schema/claude-code-settings.schema.json
var embeddedSchema []byte

// SchemaProperty is the trimmed view of one top-level schema property: the
// pieces useful for enriching an otherwise-opaque passthrough key. Deep nesting
// in the schema is intentionally ignored — only the top-level shape is indexed.
type SchemaProperty struct {
	Name        string   `json:"name"`
	Type        string   `json:"type,omitempty"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

// rawSchema is the minimal shape we decode the vendored schema into: only the
// top-level "properties" map matters for the index.
type rawSchema struct {
	Properties map[string]rawSchemaProperty `json:"properties"`
}

type rawSchemaProperty struct {
	// Type may be a string ("boolean") or, rarely, an array/object union. We
	// decode it as RawMessage and normalize to a single string, leaving it
	// empty for unions we don't try to render.
	Type        json.RawMessage   `json:"type"`
	Description string            `json:"description"`
	Enum        []json.RawMessage `json:"enum"`
}

var (
	schemaIndexOnce sync.Once
	schemaIndex     map[string]SchemaProperty
)

// SchemaIndex returns the lazily-parsed lookup of top-level schema property name
// to its {type, description, enum}. The vendored schema is embedded, so this
// never touches the network or disk at runtime. The map is built once and
// shared read-only thereafter.
func SchemaIndex() map[string]SchemaProperty {
	schemaIndexOnce.Do(func() {
		schemaIndex = buildSchemaIndex(embeddedSchema)
	})
	return schemaIndex
}

func buildSchemaIndex(data []byte) map[string]SchemaProperty {
	idx := map[string]SchemaProperty{}
	var rs rawSchema
	if err := json.Unmarshal(data, &rs); err != nil {
		// A malformed vendored schema yields an empty index rather than a
		// panic: enrichment degrades to the bare passthrough behavior, and the
		// drift test catches a schema that fails to parse.
		return idx
	}
	for name, p := range rs.Properties {
		idx[name] = SchemaProperty{
			Name:        name,
			Type:        normalizeSchemaType(p.Type),
			Description: p.Description,
			Enum:        normalizeEnum(p.Enum),
		}
	}
	return idx
}

// normalizeSchemaType reduces a JSON Schema "type" to a single string. A plain
// string type ("boolean") is returned as-is; a union (array/object form) is
// left empty since we only render trivial scalar types.
func normalizeSchemaType(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}

// normalizeEnum flattens the schema's enum values to strings for display. Only
// string/number/bool enum members are kept; structural members are dropped.
func normalizeEnum(raw []json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, m := range raw {
		var s string
		if err := json.Unmarshal(m, &s); err == nil {
			out = append(out, s)
			continue
		}
		// Non-string enum member (number/bool): keep its JSON literal.
		out = append(out, string(m))
	}
	return out
}

// typedTopLevelKeys returns the set of top-level JSON keys the Settings struct
// types via its `json:"..."` tags. This reuses the decode.go field-name cache.
func typedTopLevelKeys() map[string]struct{} {
	return jsonFieldNames(reflect.TypeOf(Settings{}))
}

// KeyClass is the bucket a top-level settings key falls into after classifying
// it against both our typed model and the vendored schema.
type KeyClass int

const (
	// ClassTyped: the key is recognized by our Go structs.
	ClassTyped KeyClass = iota
	// ClassPassthroughKnown: not typed, but present in the vendored schema.
	ClassPassthroughKnown
	// ClassUnknown: in neither our model nor the schema.
	ClassUnknown
)

func (c KeyClass) String() string {
	switch c {
	case ClassTyped:
		return "typed"
	case ClassPassthroughKnown:
		return "passthrough_known"
	default:
		return "unknown"
	}
}

// ClassifiedKey is one top-level settings key with its classification and, when
// the schema knows it, the enriched type/description/enum.
type ClassifiedKey struct {
	Key   string
	Class KeyClass
	// Schema is populated only for ClassPassthroughKnown keys.
	Schema *SchemaProperty
}

// ClassifyKey buckets a single top-level key against the typed model and the
// vendored schema. Typed keys win first (so a key that is both typed and in the
// schema is reported as typed, not passthrough).
func ClassifyKey(key string) ClassifiedKey {
	if _, ok := typedTopLevelKeys()[key]; ok {
		return ClassifiedKey{Key: key, Class: ClassTyped}
	}
	if sp, ok := SchemaIndex()[key]; ok {
		spCopy := sp
		return ClassifiedKey{Key: key, Class: ClassPassthroughKnown, Schema: &spCopy}
	}
	return ClassifiedKey{Key: key, Class: ClassUnknown}
}

// SchemaCoverage summarizes how the top-level keys present across a settings
// view split between typed, schema-known passthrough, and genuinely unknown.
type SchemaCoverage struct {
	Typed            int `json:"typed"`
	PassthroughKnown int `json:"passthroughKnown"`
	Unknown          int `json:"unknown"`
	// Total is the count of distinct top-level keys observed.
	Total int `json:"total"`
}

// ClassifyKeys classifies a set of distinct top-level keys, returning the
// per-key detail (sorted by key for stable output) and the bucket counts. Only
// non-typed keys carry schema detail; typed keys are counted but not detailed,
// since the typed views already render them richly.
func ClassifyKeys(keys []string) ([]ClassifiedKey, SchemaCoverage) {
	seen := map[string]struct{}{}
	var distinct []string
	for _, k := range keys {
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		distinct = append(distinct, k)
	}
	sort.Strings(distinct)

	var cov SchemaCoverage
	out := make([]ClassifiedKey, 0, len(distinct))
	for _, k := range distinct {
		ck := ClassifyKey(k)
		out = append(out, ck)
		cov.Total++
		switch ck.Class {
		case ClassTyped:
			cov.Typed++
		case ClassPassthroughKnown:
			cov.PassthroughKnown++
		default:
			cov.Unknown++
		}
	}
	return out, cov
}

// intentionalPassthrough lists top-level schema keys that we deliberately do
// NOT type in our Go model and accept as schema-known passthrough. These are
// cosmetic, plugin, telemetry, and other keys the settings browser does not
// interpret — it round-trips them verbatim and (via the schema index) can show
// their type + description without promoting them to first-class fields.
//
// The drift test (schema_test.go) asserts that every top-level property in the
// vendored schema is either typed by the model OR present here; a schema key
// that is neither FAILS the test, forcing a conscious decision (type it, or add
// it here) whenever the schema is refreshed or Anthropic adds a key.
//
// Security/permission-adjacent keys must NOT be parked here — promote them into
// the typed Permissions/Sandbox model instead so they render with provenance.
var intentionalPassthrough = []string{
	"agent",
	"allowManagedHooksOnly",
	"allowManagedPermissionRulesOnly", // also nested under permissions in our model
	"allowedChannelPlugins",
	"allowedHttpHookUrls",
	"autoMemoryDirectory",
	"autoMemoryEnabled",
	"autoMode",
	"autoUpdatesChannel",
	"availableModels",
	"awsAuthRefresh",
	"awsCredentialExport",
	"blockedMarketplaces",
	"channelsEnabled",
	"claudeMdExcludes",
	"companyAnnouncements",
	"defaultShell",
	"disableDeepLinkRegistration",
	"disableSkillShellExecution",
	"enabledPlugins",
	"extraKnownMarketplaces",
	"fastMode",
	"fastModePerSessionOptIn",
	"feedbackSurveyRate",
	"fileSuggestion",
	"forceLoginOrgUUID",
	"forceRemoteSettingsRefresh",
	"httpHookAllowedEnvVars",
	"includeGitInstructions",
	"minimumVersion",
	"modelOverrides",
	"otelHeadersHelper",
	"parentSettingsBehavior",
	"plansDirectory",
	"pluginConfigs",
	"pluginTrustMessage",
	"prUrlTemplate",
	"prefersReducedMotion",
	"respectGitignore",
	"showClearContextOnPlanAccept",
	"showThinkingSummaries",
	"showTurnDuration",
	"skillOverrides",
	"skipWebFetchPreflight",
	"skippedMarketplaces",
	"skippedPlugins",
	"spinnerTipsEnabled",
	"spinnerTipsOverride",
	"spinnerVerbs",
	"strictKnownMarketplaces",
	"strictPluginOnlyCustomization",
	"subagentStatusLine",
	"teammateMode",
	"terminalProgressBarEnabled",
	"tui",
	"useAutoModeDuringPlan",
	"viewMode",
	"voiceEnabled",
	"worktree",
	"wslInheritsWindowsSettings",
}

// intentionalPassthroughSet is the allowlist as a lookup.
func intentionalPassthroughSet() map[string]struct{} {
	set := make(map[string]struct{}, len(intentionalPassthrough))
	for _, k := range intentionalPassthrough {
		set[k] = struct{}{}
	}
	return set
}

// rawTopLevelKeys extracts the literal top-level object keys from a settings
// file's bytes, tolerating JWCC (comments / trailing commas) so the on-disk key
// set is recovered exactly — including typed keys, which the post-decode
// Settings value cannot enumerate field-by-field. Returns nil on parse failure.
func rawTopLevelKeys(raw []byte) []string {
	trimmed := trimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	std, err := hujson.Standardize(append([]byte(nil), trimmed...))
	if err != nil {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(std, &m); err != nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ClassifySources classifies every distinct top-level key across all parsed
// source files (using their raw bytes for an exact on-disk key set) into the
// three buckets, returning the per-key detail and the coverage counts. Keys are
// de-duplicated across scopes, so a key set in two files is counted once.
func ClassifySources(sources []SourceFile) ([]ClassifiedKey, SchemaCoverage) {
	var keys []string
	for _, sf := range sources {
		if len(sf.Raw) > 0 {
			keys = append(keys, rawTopLevelKeys(sf.Raw)...)
			continue
		}
		// No raw bytes (e.g. a CLI layer): fall back to the Unknown bag.
		if sf.Settings != nil {
			for k := range sf.Settings.Unknown {
				keys = append(keys, k)
			}
		}
	}
	return ClassifyKeys(keys)
}
