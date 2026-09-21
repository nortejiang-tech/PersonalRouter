# Security and privacy

PersonalRouter is designed for personal self-hosting, but a public source tree
must still be treated as fully readable by an untrusted party.

## Never commit

- API keys, OAuth tokens, cookies, private keys, tunnel credentials, recovery
  material, or administrator/caller credentials.
- Production hostnames, private IPs, VPN addresses, filesystem paths,
  database copies, request logs, screenshots, or backup identifiers.
- Prompt text, source code sent through the gateway, response bodies, or
  provider error bodies.

Fixtures may contain obviously fake values when a test needs them. They must
never resemble or be derived from a real credential.

## Runtime rules

- Bind local development listeners to loopback.
- Keep the management listener separate from inference listeners.
- Store only hashes or encrypted provider material in persistent state.
- Use explicit model permission and local-only enforcement.
- Do not introduce silent cross-provider fallback or post-stream retries.
- Treat missing usage and account-wide provider quotas as unknown.
- Redact upstream errors before returning them to callers.

## Reporting

Do not open a public issue containing a secret or a production trace. If a
credential may have been exposed, stop using it and rotate it through the
authorized provider interface before discussing the incident. Report a
security issue privately to the project maintainer with the smallest
reproducible description.
