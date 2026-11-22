# Portable skill compiler

Parses UTF-8 SKILL.md files with a 1 MiB bound using official YAML v3. Standard
front matter preserves unknown fields in `standardFrontmatterJson`. A missing
front matter block infers the parent-directory name and records conservative
runtime defaults/clarification fields; malformed or unclosed blocks fail.

Validation preserves slug/directory/description constraints and duplicate-name
checks. Standard skills default to resources. Explicit `expose_as` supports tools,
resources, prompts and the guidance alias. Source policies and runtime overrides
apply to portable canonical fields, with checksum staleness handled by the sync
store. Compiled status preserves publication review requirements for high risk,
missing descriptions, empty bodies and absent routing metadata.

Ancillary files are limited to 128 files, 256 KiB each, 2 MiB per package; nested
skill packages and hidden trees are excluded. Checksums are lowercase SHA-256
hex compatible with Swift and the MCP package reader.

Canonical JSON field names match Swift `CompiledSkillDocument` schema version 1.
Clarification questions currently use generic text/options; their missing-field
and required state remain explicit. The line-oriented body diff matches Swift's
LCS tie-breaking and bounded/truncated large-file behavior.
