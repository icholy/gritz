package workspace_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/icholy/gritz/internal/runner/workspace"
	"gotest.tools/v3/assert"
)

// writeConfig writes cfg to a temp workspaces.yaml and returns its path.
func writeConfig(t *testing.T, cfg string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workspaces.yaml")
	assert.NilError(t, os.WriteFile(path, []byte(cfg), 0644))
	return path
}

// TestLoadConfig_Secrets pins that secrets: values go through the same
// ${env:}/${sh:} expansion as the rest of the config — the declaration is what
// marks a value secret, not a separate resolution path.
func TestLoadConfig_Secrets(t *testing.T) {
	t.Parallel()
	// Arrange
	path := writeConfig(t, `
workspaces:
  test:
    secrets:
      GH_TOKEN: ${sh:gh auth token}
      NPM_TOKEN: ${env:NPM_TOKEN}
    container:
      image: alpine:latest
`)
	expand := func(namespace, value string) (string, error) {
		return fmt.Sprintf("expanded-%s-%s", namespace, value), nil
	}

	// Act
	cfg, err := workspace.LoadConfig(path, expand)

	// Assert
	assert.NilError(t, err)
	ws, err := cfg.Get("test")
	assert.NilError(t, err)
	assert.DeepEqual(t, ws.Secrets, map[string]string{
		"GH_TOKEN":  "expanded-sh-gh auth token",
		"NPM_TOKEN": "expanded-env-NPM_TOKEN",
	})
	assert.DeepEqual(t, ws.SecretNames(), []string{"GH_TOKEN", "NPM_TOKEN"})
}

func TestWorkspaceValidate_Secrets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ws   workspace.Workspace
		err  string
	}{
		{
			name: "valid",
			ws: workspace.Workspace{
				Secrets: map[string]string{"GH_TOKEN": "gho_supersecretvalue"},
			},
		},
		{
			name: "reserved name",
			ws: workspace.Workspace{
				Secrets: map[string]string{"GRITZ_TOKEN": "gho_supersecretvalue"},
			},
			err: "secrets.GRITZ_TOKEN: GRITZ_* names are reserved",
		},
		{
			name: "empty value",
			ws: workspace.Workspace{
				Secrets: map[string]string{"GH_TOKEN": ""},
			},
			err: "secrets.GH_TOKEN: value is empty",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.ws.Validate()
			if tt.err == "" {
				assert.NilError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.err)
		})
	}
}
