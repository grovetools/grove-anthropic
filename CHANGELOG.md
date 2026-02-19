## v0.6.1 (2026-02-19)

Configuration handling has been enhanced with new JSON schema tags (e53bf14) that provide metadata for API key fields. These tags include sensitivity markers, priority ordering, and usage hints to improve tooling support and validation.

### Features

* Add jsonschema tags for API key config (e53bf14)

### File Changes

```
 pkg/config/api_key.go | 4 ++--
 1 file changed, 2 insertions(+), 2 deletions(-)
```

## v0.6.0 (2026-02-02)

This release introduces XDG-compliant logging paths (6528197) and migrates the project configuration from YAML to TOML format (6e935b6). Additionally, the module dependencies have been updated to reflect the migration to the grovetools organization (fe84cdf), and the MIT License has been formally added (ebd671a).

Documentation tooling has been enhanced with new configuration for the package (11c87a2), alongside the relocation of rule files to a dedicated directory (08fce38) and the removal of obsolete configuration files (ff2c59e). The README and overview documentation have also been updated (178a6af).

Fixes include correcting the version package path to ensure binaries report the correct commit hash (f13090b) and restoring the CI release workflow (8f940a7).

### Features
* Update readme/overview (178a6af)
* Add docgen configuration for grove-anthropic package (11c87a2)

### Bug Fixes
* Update VERSION_PKG to grovetools/core path (f13090b)

### Refactoring
* Migrate logging to XDG-compliant paths (6528197)

### Chores
* Restore release workflow (8f940a7)
* Add MIT License (ebd671a)
* Migrate grove.yml to grove.toml (6e935b6)
* Remove docgen files from repo (ff2c59e)
* Move docs.rules to .cx/ directory (08fce38)
* Update go.mod for grovetools migration (fe84cdf)

### File Changes
```
 .cx/docs.rules                |  12 +++++
 .github/workflows/release.yml | 123 ++++++++++++++----------------------------
 LICENSE                       |  21 ++++++++
 Makefile                      |   2 +-
 README.md                     |  57 +++++++++++---------
 docs/01-overview.md           |  36 +++++++++++++
 go.mod                        |  11 +++-
 go.sum                        |  53 +++++++++++++++---
 grove.toml                    |   8 +++
 grove.yml                     |   7 ---
 pkg/docs/docs.json            |   3 ++
 pkg/logging/query_log.go      |   9 ++--
 12 files changed, 214 insertions(+), 128 deletions(-)
```

# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Initial implementation of grove-anthropic
- Basic command structure
- E2E test framework
