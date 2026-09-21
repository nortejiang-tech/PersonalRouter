# Public architecture baseline

PersonalRouter separates the stable gateway contract from provider-specific
authentication and protocol details.

~~~text
client
  -> caller authentication and model permission
  -> protocol handler (chat / responses / messages)
  -> provider route or fixed adapter
  -> upstream model service

management listener
  -> administrator authentication
  -> provider, model, caller, and usage metadata

SQLite
  -> encrypted provider secret material
  -> hashed caller/admin credentials
  -> model and permission metadata
  -> request metadata and nullable usage
~~~

The Go service owns caller authentication, model authorization, request
accounting, bounded request handling, and the public protocol surface. A
separately versioned adapter can handle an upstream login or protocol
conversion when that path has been reviewed. The adapter never accepts an
arbitrary route supplied by an inference caller.

The React application is a client of the protected management API. It keeps
administrator input in memory, never places credentials in URLs, and renders
unknown usage as unknown instead of inventing a number.

The runtime accepts one non-secret JSON configuration containing listener
addresses, data paths, static web path, and adapter address. Secret files and
encrypted values are separate from that configuration. Production service
units, tunnel settings, firewall rules, host addresses, and recovery assets
are intentionally outside this public snapshot.

## Request lifecycle

1. Identify the listener as LAN, public inference, or management. The route
   cannot be selected by an untrusted request header.
2. Authenticate the caller or administrator against the appropriate listener.
3. Check caller scope, local-only policy, enabled state, model permission, and
   protocol compatibility.
4. Persist request metadata before an upstream attempt so interruption is
   visible as an incomplete record.
5. Send one explicit upstream request with server-held credentials. Client
   credentials are never forwarded upstream.
6. Flush streaming events promptly, collect bounded usage metadata when the
   protocol provides it, and finalize the record.

Automatic cross-provider fallback is disabled by design. Once a stream has
started, the gateway never hides an upstream failure by retrying or changing
models.
