package command

import (
	"testing"

	"gotest.tools/v3/assert"
)

// TestDriverSecrets asserts the driver picks up the names the runner declared in
// GRITZ_SECRETS, reads their values from its own environment, and adds its own
// task token under the "token" name it is masked as.
func TestDriverSecrets(t *testing.T) {
	// Arrange - the environment the runner injects for a workspace with two
	// declared secrets. GRITZ_TOKEN is set but undeclared, so it is not masked.
	t.Setenv("GRITZ_SECRETS", "GH_TOKEN,NPM_TOKEN")
	t.Setenv("GH_TOKEN", "ghp_abc123")
	t.Setenv("NPM_TOKEN", "npm_xyz789")
	t.Setenv("GRITZ_TOKEN", "jwt")

	// Act
	secrets := driverSecrets("jwt")

	// Assert
	assert.DeepEqual(t, secrets, map[string]string{
		"GH_TOKEN":  "ghp_abc123",
		"NPM_TOKEN": "npm_xyz789",
		"token":     "jwt",
	})
}

// TestDriverSecrets_NoSecrets asserts an undeclared workspace still masks the
// task token, and that an unset GRITZ_SECRETS does not produce an empty name.
func TestDriverSecrets_NoSecrets(t *testing.T) {
	// Arrange
	t.Setenv("GRITZ_SECRETS", "")

	// Act
	secrets := driverSecrets("jwt")

	// Assert
	assert.DeepEqual(t, secrets, map[string]string{"token": "jwt"})
}
