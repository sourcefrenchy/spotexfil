package c2functions

import (
	"strings"
	"testing"

	c2structs "github.com/MythicMeta/MythicContainer/c2_structs"
)

func TestDefinition(t *testing.T) {
	def := Definition()
	if def.Name != ProfileName {
		t.Fatalf("Name = %q, want %q", def.Name, ProfileName)
	}
	if def.IsP2p {
		t.Fatal("IsP2p must be false")
	}
	if !def.IsServerRouted {
		t.Fatal("IsServerRouted must be true")
	}
	if def.ConfigCheckFunction == nil || def.OPSECCheckFunction == nil {
		t.Fatal("ConfigCheck and OPSECCheck functions must be set")
	}
	if def.SemVer == "" {
		t.Fatal("SemVer must be set")
	}
}

func TestParametersIncludeAllRequiredNames(t *testing.T) {
	required := []string{
		"AESPSK",
		"callback_host",
		"spotify_username",
		"spotify_client_id",
		"spotify_client_secret",
		"spotify_redirect_uri",
		"spotify_token_file",
		"passphrase",
		"poll_interval",
	}
	params := Parameters()
	byName := make(map[string]c2structs.C2Parameter, len(params))
	for _, p := range params {
		if p.Description == "" {
			t.Errorf("parameter %q has no Description", p.Name)
		}
		byName[p.Name] = p
	}
	for _, name := range required {
		if _, ok := byName[name]; !ok {
			t.Errorf("missing parameter %q", name)
		}
	}
}

func TestAESPSKIsCryptoChooseOne(t *testing.T) {
	for _, p := range Parameters() {
		if p.Name != "AESPSK" {
			continue
		}
		if !p.IsCryptoType {
			t.Fatal("AESPSK must have IsCryptoType set")
		}
		if p.ParameterType != c2structs.C2_PARAMETER_TYPE_CHOOSE_ONE {
			t.Fatalf("AESPSK ParameterType = %v, want ChooseOne", p.ParameterType)
		}
		joined := strings.Join(p.Choices, ",")
		if !strings.Contains(joined, "aes256_hmac") || !strings.Contains(joined, "none") {
			t.Fatalf("AESPSK choices = %v", p.Choices)
		}
		return
	}
	t.Fatal("AESPSK parameter not defined")
}

func TestPassphraseDescriptionMentionsAgentMatch(t *testing.T) {
	for _, p := range Parameters() {
		if p.Name == "passphrase" {
			if !strings.Contains(strings.ToLower(p.Description), "agent") {
				t.Fatal("passphrase Description must note it must match the agent build")
			}
			return
		}
	}
	t.Fatal("passphrase parameter not defined")
}

func validParams() map[string]interface{} {
	return map[string]interface{}{
		"spotify_username":      "user",
		"spotify_client_id":     "id",
		"spotify_client_secret": "secret",
		"passphrase":            "correct horse battery staple",
		"poll_interval":         float64(30),
	}
}

func configCheckMsg(params map[string]interface{}) c2structs.C2ConfigCheckMessage {
	return c2structs.C2ConfigCheckMessage{
		C2Parameters: c2structs.C2Parameters{
			Name:       ProfileName,
			Parameters: params,
		},
	}
}

func TestConfigCheck(t *testing.T) {
	cases := []struct {
		name        string
		mutate      func(map[string]interface{})
		wantSuccess bool
	}{
		{name: "valid", mutate: nil, wantSuccess: true},
		{name: "missing username", mutate: func(m map[string]interface{}) { delete(m, "spotify_username") }, wantSuccess: false},
		{name: "empty client id", mutate: func(m map[string]interface{}) { m["spotify_client_id"] = "  " }, wantSuccess: false},
		{name: "missing client secret", mutate: func(m map[string]interface{}) { delete(m, "spotify_client_secret") }, wantSuccess: false},
		{name: "missing passphrase", mutate: func(m map[string]interface{}) { delete(m, "passphrase") }, wantSuccess: false},
		{name: "poll interval too low", mutate: func(m map[string]interface{}) { m["poll_interval"] = float64(10) }, wantSuccess: false},
		{name: "poll interval at minimum", mutate: func(m map[string]interface{}) { m["poll_interval"] = float64(20) }, wantSuccess: true},
		{name: "missing poll interval", mutate: func(m map[string]interface{}) { delete(m, "poll_interval") }, wantSuccess: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := validParams()
			if tc.mutate != nil {
				tc.mutate(params)
			}
			resp := configCheck(configCheckMsg(params))
			if resp.Success != tc.wantSuccess {
				t.Fatalf("Success = %v, want %v (error: %q)", resp.Success, tc.wantSuccess, resp.Error)
			}
			if !resp.Success && resp.Error == "" {
				t.Fatal("failed check must include an Error message")
			}
		})
	}
}

func TestOPSECCheckAlwaysSucceeds(t *testing.T) {
	params := validParams()
	params["passphrase"] = "short"
	resp := opsecCheck(c2structs.C2OPSECMessage{
		C2Parameters: c2structs.C2Parameters{
			Name:       ProfileName,
			Parameters: params,
		},
	})
	if !resp.Success {
		t.Fatal("OPSEC check must not block builds")
	}
	if resp.Message == "" {
		t.Fatal("OPSEC check should return advisory notes")
	}
	if !strings.Contains(resp.Message, "short") {
		t.Fatal("short passphrase should be flagged")
	}
}
