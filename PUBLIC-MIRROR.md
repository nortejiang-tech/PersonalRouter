# Public mirror scope

This repository is an intentionally sanitized public snapshot of PersonalRouter.
The private repository remains the canonical maintenance source. Public
updates happen only when the maintainer explicitly requests a sync.

## Export policy

Included:

- Core Go source and tests.
- React/TypeScript source and tests.
- Dependency lockfiles.
- Generic API, architecture, development, security, and provider-reference
  documentation.

Excluded:

- Private instruction files and task handoffs.
- Production deployment units, tunnel/DNS/firewall helpers, and recovery
  scripts.
- Prompt transcripts, Coder receipts, internal reports, screenshots, and
  environment-specific acceptance evidence.
- Host addresses, private domains, user-specific caller/provider inventories,
  backup IDs, live hashes, credential paths, keys, tokens, and databases.

Every publication should be rebuilt as a fresh history from an explicit
allowlist. Before pushing, run the Go and web checks, inspect the exact file
list, scan the complete export for credentials and private infrastructure
identifiers, and verify that the destination repository is public only after
the sanitized commit is ready.
