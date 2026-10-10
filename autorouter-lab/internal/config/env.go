package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// APIKeyEnv is the only source of the API key. There is deliberately no .env
// file, no config-file lookup and no command-line flag: process arguments are
// visible through ps and shell history, and a file that cannot exist cannot be
// committed.
const APIKeyEnv = "OPENROUTER_API_KEY" // #nosec G101 -- the name of an environment variable, not a credential

// ErrNoAPIKey means the environment variable is unset or blank.
var ErrNoAPIKey = errors.New("API key not set")

// APIKey reads the key from the environment. The harness needs the value only
// to teach the log redactor what to scrub; the OpenRouter SDK reads the same
// variable itself, so the key never passes through request-building code.
func APIKey() (string, error) {
	v, ok := os.LookupEnv(APIKeyEnv)
	if !ok || strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("%w: %s is unset or empty; run `export %s=...` and try again",
			ErrNoAPIKey, APIKeyEnv, APIKeyEnv)
	}
	return strings.TrimSpace(v), nil
}
