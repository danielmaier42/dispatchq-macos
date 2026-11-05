package provider

import (
	"fmt"
	"slices"
	"strings"
)

// reservedEnv are the variables dispatchq sets itself from other fields of the
// instance. Letting the environment list override them would make one setting
// silently beat another, so they are refused with a pointer to the field that
// owns them.
var reservedEnv = map[string]string{
	"CLAUDE_CONFIG_DIR": "the configuration directory",
	"CODEX_HOME":        "the configuration directory",
	"XDG_CONFIG_HOME":   "the configuration directory",
	"XDG_DATA_HOME":     "the configuration directory",
	"XDG_CACHE_HOME":    "the configuration directory",
}

// CheckEnvName reports whether name can be an environment variable of a
// provider: the portable shell form, and not one dispatchq sets itself.
func CheckEnvName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: an environment variable needs a name", ErrInvalidProvider)
	}
	for i, r := range name {
		ok := r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '_' || i > 0 && r >= '0' && r <= '9'
		if !ok {
			return fmt.Errorf("%w: environment variable %q may only contain letters, digits and '_' (and must not start with a digit)",
				ErrInvalidProvider, name)
		}
	}
	if field, ok := reservedEnv[name]; ok {
		return fmt.Errorf("%w: %s is set from %s; use that field instead", ErrInvalidProvider, name, field)
	}
	upper := strings.ToUpper(name)
	if strings.HasPrefix(upper, "DISPATCHQ_") || strings.HasPrefix(upper, "CLAUDEQ_") {
		return fmt.Errorf("%w: %s is reserved for dispatchq's own variables", ErrInvalidProvider, name)
	}
	return nil
}

// checkEnv validates every variable of an instance.
func checkEnv(env map[string]string) error {
	for name, value := range env {
		if err := CheckEnvName(name); err != nil {
			return err
		}
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("%w: the value of %s contains a NUL byte", ErrInvalidProvider, name)
		}
	}
	return nil
}

// EnvNames lists the instance's variable names, sorted. It is what the app and
// the CLI show: the values are secrets and stay in the file.
func (i Instance) EnvNames() []string {
	names := make([]string, 0, len(i.Env))
	for name := range i.Env {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// EnvPairs is the instance's own variables as NAME=value entries, in a stable
// order.
func (i Instance) EnvPairs() []string {
	names := i.EnvNames()
	pairs := make([]string, len(names))
	for n, name := range names {
		pairs[n] = name + "=" + i.Env[name]
	}
	return pairs
}

// Environ is the environment additions for a harness process: base (what the
// adapter derives from other fields, such as the configuration directory) with
// the instance's own variables after it, so the operator's explicit value wins
// when a process receives the same name twice.
func (i Instance) Environ(base ...string) []string {
	return append(slices.Clone(base), i.EnvPairs()...)
}

// UsesAPIKey reports whether the instance is given an Anthropic credential
// through its environment. Such an instance is billed per token, so the plan
// limits of the login it might also hold do not apply to it.
func (i Instance) UsesAPIKey() bool {
	return i.Env["ANTHROPIC_API_KEY"] != "" || i.Env["ANTHROPIC_AUTH_TOKEN"] != ""
}
