package config

import (
	"strings"
	"testing"
)

func TestMCPBackendsCredentialsAndAllowlist(t *testing.T) {
	t.Setenv("TEST_MCP_TOKEN", "independent-backend-test-token-123")
	valid := `[{"id":"analysis","url":"http://127.0.0.1:17890/mcp","token_env":"TEST_MCP_TOKEN","tools":[{"name":"compute","policy":"analysis"}]}]`
	c := Client{MCPBackendsJSON: valid, RemoteToken: "remote-test-token-1234567890123456"}
	backends, err := c.MCPBackends()
	if err != nil || len(backends) != 1 || backends[0].TimeoutSeconds != 30 {
		t.Fatal(backends, err)
	}
	for _, bad := range []string{
		strings.Replace(valid, `"analysis"}`, `"write"}`, 1),
		strings.Replace(valid, "http://127.0.0.1:17890/mcp", "http://user:password@127.0.0.1/mcp", 1),
		strings.Replace(valid, "http://127.0.0.1:17890/mcp", "http://127.0.0.1/../mcp", 1),
		strings.Replace(valid, `"compute"`, `"../compute"`, 1),
		strings.Replace(valid, `"token_env"`, `"literal_token"`, 1),
	} {
		c.MCPBackendsJSON = bad
		if _, err = c.MCPBackends(); err == nil {
			t.Fatal("bad config accepted", bad)
		}
	}
	c.MCPBackendsJSON = valid
	c.RemoteToken = "independent-backend-test-token-123"
	if _, err = c.MCPBackends(); err == nil {
		t.Fatal("credential reuse accepted")
	}
}

func TestRemoteMCPListenerCanBeLoopbackOnly(t *testing.T) {
	c := Client{Listen: "127.0.0.1:17600", RemoteHost: "127.0.0.1", RemotePort: "17602", RemotePortConfigured: true, RemoteToken: "remote-test-token-1234567890123456"}
	address, err := c.RemoteAddress()
	if err != nil || address != "127.0.0.1:17602" {
		t.Fatal(address, err)
	}
	c.RemoteHost = "untrusted.example"
	if _, err = c.RemoteAddress(); err == nil {
		t.Fatal("arbitrary bind hostname accepted")
	}
}
