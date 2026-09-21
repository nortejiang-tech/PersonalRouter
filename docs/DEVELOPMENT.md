# Public development guide

The private maintenance repository contains deployment history and
environment-specific operations. This public guide covers the reproducible
software surface only.

## Repository layout

- cmd/personalrouter: process entry point.
- internal/runtime: configuration, listeners, lifecycle, health, and watchdog
  integration.
- internal/gateway: protocol routing, caller authorization, streaming, and
  upstream request handling.
- internal/store: SQLite schema and persistence.
- internal/security: credential hashing, encrypted provider material, and
  request-key parsing.
- internal/admin: protected management handlers and connectivity checks.
- internal/usage: request accounting and summaries.
- web: React/TypeScript management application.

## Verification

~~~bash
go test ./...
go test -race ./...
go vet ./...
npm --prefix web ci
npm --prefix web run build
npm --prefix web run test
npm --prefix web run test:lib
~~~

Tests use loopback fixtures and fake credentials. They must not be pointed at
real provider accounts or production listeners as part of an ordinary build.

## Configuration

The process accepts an absolute path to a JSON file containing listener
addresses, data directory, static web directory, and an optional adapter
address. Listener validation rejects wildcard and public addresses for the
public origin, rejects ambiguous ports, and requires absolute paths for
filesystem locations.

Keep credentials outside source control. A local configuration should use
loopback addresses and a disposable SQLite directory. Do not copy a
production database, key file, cookie, provider token, or tunnel credential
into a test fixture.

## Changes

Changes to the public snapshot are reviewed separately from private
deployment maintenance. A public update must identify its private source
revision, list the exported paths, run the offline tests, and repeat the
secret scan before publication.
