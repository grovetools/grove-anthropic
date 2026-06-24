# Vendored Claude Code Settings schema

`claude-code-settings.schema.json` is a vendored snapshot of the community
"Claude Code Settings" JSON Schema published by SchemaStore:

  https://www.schemastore.org/claude-code-settings.json

## Why it is vendored

The schema is **community-maintained and NOT version-pinned** to any specific
Claude Code release. It is not an official Anthropic artifact, so it can change
at any time and has no version field to key off of. We therefore vendor a
snapshot rather than fetch it at runtime: a settings browser must behave
deterministically and offline, and a moving upstream schema would make the
classification of "known passthrough" vs "unknown" keys non-reproducible.

The snapshot is embedded into the binary with `go:embed` (see
`../schema.go`) and only ever read locally.

## What it is used for

`pkg/ccsettings/schema.go` parses the **top-level `properties`** of this schema
into a `key -> {type, description, enum}` index. That index enriches settings
keys our Go model does not type: instead of an opaque "unknown key" count, the
settings browser can show the schema's type and description for keys that are
merely passthrough (present in the schema) and distinguish them from keys that
are genuinely unknown (in neither our model nor the schema).

A drift test (`schema_test.go`) asserts that every top-level property in this
schema is either typed by our Go model or listed in an explicit
`intentionalPassthrough` allowlist, so refreshing this file forces a conscious
classification of any newly added keys.

## How to refresh

1. Re-download the snapshot:

   ```sh
   curl -sSL https://www.schemastore.org/claude-code-settings.json \
     -o pkg/ccsettings/schema/claude-code-settings.schema.json
   ```

2. Run the package tests:

   ```sh
   go test ./pkg/ccsettings/
   ```

3. If `TestSchemaDrift` fails, the upstream schema added top-level keys we do
   not yet classify. For each new key, either add a typed field to the model in
   `types.go` or add the key to the `intentionalPassthrough` allowlist in
   `schema.go`, then re-run the tests.
