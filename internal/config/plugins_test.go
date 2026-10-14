package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPluginEnvironmentValidation(t *testing.T) {
	t.Setenv("PLUGIN_FIXTURE_TOKEN", "fixture-backend-secret")
	valid := `[{"id":"analysis","url":"http://127.0.0.1:17890","token_env":"PLUGIN_FIXTURE_TOKEN","routes":[{"method":"GET","path":"/v1/capabilities"},{"method":"POST","path":"/v1/scans"},{"method":"GET","path":"/v1/jobs/{id}"}]}]`
	c := Client{PluginsJSON: valid}
	p, err := c.Plugins()
	if err != nil || len(p) != 1 || p[0].TimeoutSeconds != 30 {
		t.Fatalf("parse: %v", err)
	}
	encoded, _ := json.Marshal(p)
	if strings.Contains(string(encoded), "fixture-backend-secret") {
		t.Fatal("token serialized")
	}
	if err = c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{`, valid + ` {}`, strings.Replace(valid, "analysis", "../secret", 1), strings.Replace(valid, "http://127.0.0.1:17890", "https://user:pass@example.test", 1),
		strings.Replace(valid, `"method":"POST"`, `"method":"DELETE"`, 1), strings.Replace(valid, "/v1/jobs/{id}", "/v1/../secrets", 1),
		strings.Replace(valid, "PLUGIN_FIXTURE_TOKEN", "NOT_SET_TOKEN", 1), strings.Replace(valid, `"routes":`, `"token":"leak","routes":`, 1),
	} {
		if _, err := (Client{PluginsJSON: bad}).Plugins(); err == nil {
			t.Errorf("accepted invalid config %q", bad)
		}
	}
	if _, err := (Client{PluginsJSON: valid, RemoteToken: "fixture-backend-secret"}).Plugins(); err == nil {
		t.Fatal("shared remote secret accepted")
	}
	t.Setenv("GO_CLIENT_PLUGINS", valid)
	if len(ClientFromEnv().PluginsJSON) == 0 {
		t.Fatal("env not loaded")
	}
	if p, err := (Client{}).Plugins(); err != nil || len(p) != 0 {
		t.Fatal("absence must disable")
	}
}
