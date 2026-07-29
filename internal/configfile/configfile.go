package configfile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Dir returns the gritz config directory.
// It checks GRITZ_CONFIG_DIR first, then falls back to
// os.UserConfigDir()/gritz (e.g. ~/.config/gritz on Linux).
func Dir() (string, error) {
	if dir := os.Getenv("GRITZ_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config directory: %w", err)
	}
	return filepath.Join(dir, "gritz"), nil
}

// Path returns the path to the gritz config file.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Overrides contains values that take priority over the config file.
// If all fields are set, the config file is not read.
type Overrides struct {
	Token string
}

func (o *Overrides) complete() bool {
	return o != nil && o.Token != ""
}

func (o *Overrides) apply(f *File) error {
	if o == nil {
		return nil
	}
	if o.Token != "" {
		f.Token = o.Token
	}
	return nil
}

// Load reads the config file and applies any overrides.
// Returns a non-nil File even if the file doesn't exist.
func Load(overrides *Overrides) (*File, error) {
	if overrides.complete() {
		var f File
		if err := overrides.apply(&f); err != nil {
			return nil, err
		}
		return &f, nil
	}
	p, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return &File{}, nil
		}
		return nil, err
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	if err := overrides.apply(&f); err != nil {
		return nil, err
	}
	return &f, nil
}

// Save writes the config file.
func Save(f *File) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0600)
}

// File stores the gritz configuration.
type File struct {
	Token string `json:"token"`
}
