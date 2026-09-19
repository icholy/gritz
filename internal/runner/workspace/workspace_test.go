package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
)

// stubExpand stands in for ExpandVar so the examples' ${env:}/${sh:} references
// resolve to a marker instead of reaching the test host's environment or
// running `gh auth token`.
func stubExpand(namespace, value string) (string, error) {
	return fmt.Sprintf("<%s:%s>", namespace, value), nil
}

// TestLoadConfig_Examples asserts every checked-in example workspace parses and
// validates, so a doc example can't drift into config the runner would reject.
func TestLoadConfig_Examples(t *testing.T) {
	// Arrange
	paths, err := filepath.Glob("../../../examples/workspaces/*.yml")
	assert.NilError(t, err)
	assert.Assert(t, len(paths) > 0, "no example workspaces found")

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			// Act
			cfg, err := LoadConfig(path, stubExpand)

			// Assert
			assert.NilError(t, err)
			assert.NilError(t, cfg.Validate())
		})
	}
}

// TestLoadConfig_DefaultYAML asserts the template `gritz runner` writes on first
// start loads, and that it puts its credential under secrets: -- the pattern the
// README documents -- rather than in container.environment, where the value
// would ship to the server unmasked.
func TestLoadConfig_DefaultYAML(t *testing.T) {
	// Arrange
	path := filepath.Join(t.TempDir(), "workspaces.yaml")
	assert.NilError(t, os.WriteFile(path, []byte(defaultYAML), 0644))

	// Act
	cfg, err := LoadConfig(path, stubExpand)

	// Assert
	assert.NilError(t, err)
	assert.NilError(t, cfg.Validate())
	ws := cfg.Workspaces["pets-workshop"]
	assert.DeepEqual(t, ws.SecretNames(), []string{"CLAUDE_CODE_OAUTH_TOKEN"})
	assert.Equal(t, len(ws.Container.Environment), 0)
}

// TestLoadConfig_PrivateRepoExample pins the documented private-repo pattern:
// the token is declared as a secret, and the clone command references it by
// variable so the sandbox shell -- not the config loader -- expands it, leaving
// the command string the driver logs free of the value.
func TestLoadConfig_PrivateRepoExample(t *testing.T) {
	// Act
	cfg, err := LoadConfig("../../../examples/workspaces/private-repo.yml", stubExpand)

	// Assert
	assert.NilError(t, err)
	ws := cfg.Workspaces["pets-workshop"]
	assert.DeepEqual(t, ws.SecretNames(), []string{"GH_TOKEN"})
	assert.DeepEqual(t, ws.Commands, []string{
		"git clone https://x-access-token:${GH_TOKEN}@github.com/private/repo.git",
	})
}
