package provider

import (
	"errors"
	"reflect"
	"testing"
)

func TestCheckEnvName(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{"ANTHROPIC_API_KEY", true},
		{"_x1", true},
		{"lower_case", true},
		{"", false},
		{"1ABC", false},
		{"HAS-DASH", false},
		{"HAS SPACE", false},
		{"A=B", false},
		{"CLAUDE_CONFIG_DIR", false},
		{"CODEX_HOME", false},
		{"XDG_DATA_HOME", false},
		{"DISPATCHQ_HOME", false},
		{"claudeq_bin", false},
	}
	for _, tc := range tests {
		err := CheckEnvName(tc.name)
		if (err == nil) != tc.ok {
			t.Errorf("CheckEnvName(%q) = %v, want ok=%t", tc.name, err, tc.ok)
		}
		if err != nil && !errors.Is(err, ErrInvalidProvider) {
			t.Errorf("CheckEnvName(%q) = %v, want ErrInvalidProvider", tc.name, err)
		}
	}
}

func TestEnvironPutsTheOperatorsVariablesAfterTheDerivedOnes(t *testing.T) {
	inst := Instance{Env: map[string]string{"B": "2", "A": "1"}}
	got := inst.Environ("CLAUDE_CONFIG_DIR=/x")
	want := []string{"CLAUDE_CONFIG_DIR=/x", "A=1", "B=2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Environ = %q, want %q", got, want)
	}
	if got := (Instance{}).Environ(); len(got) != 0 {
		t.Fatalf("Environ of an instance without variables = %q, want nothing", got)
	}
}

func TestUsesAPIKey(t *testing.T) {
	if (Instance{}).UsesAPIKey() {
		t.Fatal("an instance without variables must not count as API-key billed")
	}
	if (Instance{Env: map[string]string{"ANTHROPIC_API_KEY": ""}}).UsesAPIKey() {
		t.Fatal("an empty key blanks the variable out; it is not a credential")
	}
	if !(Instance{Env: map[string]string{"ANTHROPIC_API_KEY": "sk-ant-x"}}).UsesAPIKey() {
		t.Fatal("an instance with an API key is billed per token")
	}
}

func TestValidateRefusesABadEnvironment(t *testing.T) {
	reg := NewRegistry()
	inst := Instance{ID: "x", Kind: KindClaudeCode, Env: map[string]string{"CODEX_HOME": "/a"}}
	if err := Validate(reg, inst); err == nil {
		t.Fatal("Validate accepted a variable another field owns")
	}
}

func TestInstanceCopiesItsEnvironmentThroughTheStore(t *testing.T) {
	inst := Instance{ID: "x", Env: map[string]string{"K": "v"}}
	stored := inst.Stored()
	stored.Env["K"] = "changed"
	if inst.Env["K"] != "v" {
		t.Fatal("editing the stored form changed the instance: the map is shared")
	}
	if got := InstanceOf(inst.Stored()); !reflect.DeepEqual(got.Env, inst.Env) {
		t.Fatalf("round trip env = %v, want %v", got.Env, inst.Env)
	}
}
