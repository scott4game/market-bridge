# MCP backend aggregation

The authenticated `/mcp` endpoint preserves native market tools and can expose
administrator-configured Streamable HTTP backends. REST plugin configuration is
independent and is not converted into MCP automatically.

Set `GO_CLIENT_MCP_BACKENDS` to a JSON array, for example:

```json
[{"id":"analysis","url":"http://host.docker.internal:17890/mcp","token_env":"GO_CLIENT_MCP_BACKEND_TOKEN","timeout_seconds":30,"tools":[{"name":"compute","policy":"analysis"}]}]
```

This is a generic example: replace the endpoint and tool allowlist with names
actually advertised by your backend. The exposed name is `analysis__compute`.
Policy is descriptive metadata, not user authorization. Configure the backend
token independently of the client remote token and upstream market token.
Tokens must be at least 24 characters, without whitespace.

`deploy/compose.client.yaml` passes `GO_CLIENT_MCP_BACKENDS` and
`GO_CLIENT_MCP_BACKEND_TOKEN` into the container. Additional `token_env` names
must also be explicitly passed through Compose; a host `.env` alone does not
inject arbitrary variables. Keep credentials out of Git. Recreate the client
container after changing environment configuration.

Container loopback is not the host. Use a restricted, container-reachable
backend bind address and firewall; do not publish the backend on the Internet.
The frontend remote token authenticates the HTTPS gateway only. Backend tokens
are attached independently, cookies are not forwarded, redirects are refused,
and tool submissions are not automatically retried.

Discovery refreshes every 60 seconds. `get_mcp_backend_status` reports backend
availability. Failed discovery removes backend tools without removing native
market tools. Retest discovery in the client after refreshing the gateway.
An absent status tool suggests an older image; an unavailable backend suggests
an address, credential, allowlist or backend service problem.

## Image publication

Pushes to `dev` trigger GitHub Actions Release. A dev commit with the exact
trailer `Release-Components: client` publishes only the client image. Other
pushes and version tags retain the existing client/server publication behavior.
Images receive `latest` and an explicit `dev-<12-character-commit>` tag (or the
version tag). Prefer the commit tag or digest when deploying and rolling back.
Publication does not update running containers.
