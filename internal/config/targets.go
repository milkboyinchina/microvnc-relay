package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// TargetsConfigFile represents the top-level structure of targets.yaml.
type TargetsConfigFile struct {
	Targets []DeviceConfig `yaml:"targets"`
}

// ResolveTargetsPath determines the path to targets.yaml based on configPath and explicit override.
func ResolveTargetsPath(configPath, explicitOverride string) string {
	if strings.TrimSpace(explicitOverride) != "" {
		if filepath.IsAbs(explicitOverride) {
			return explicitOverride
		}
		if configPath != "" {
			return filepath.Join(filepath.Dir(configPath), explicitOverride)
		}
		return explicitOverride
	}

	if configPath != "" {
		return filepath.Join(filepath.Dir(configPath), "targets.yaml")
	}

	return "./targets.yaml"
}

// LoadTargetsFile reads and parses a standalone targets.yaml file.
func LoadTargetsFile(path string) ([]DeviceConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var tf TargetsConfigFile
	if err := yaml.Unmarshal(data, &tf); err != nil {
		return nil, fmt.Errorf("parse targets yaml %s: %w", path, err)
	}

	return tf.Targets, nil
}

// SaveTargetsFile atomically writes targets to the specified path without touching config.yaml.
func SaveTargetsFile(path string, targets []DeviceConfig) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("targets file path cannot be empty")
	}

	tf := TargetsConfigFile{Targets: targets}
	data, err := yaml.Marshal(&tf)
	if err != nil {
		return fmt.Errorf("marshal targets to yaml: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("create targets dir: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, "targets-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary targets file: %w", err)
	}
	tmpName := tmpFile.Name()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temp targets: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("sync temp targets: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp targets: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("atomic rename targets: %w", err)
	}

	return nil
}
