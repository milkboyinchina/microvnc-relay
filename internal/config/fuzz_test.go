package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// FuzzConfigYAML exercises the daemon config parser and validator. It must
// never panic on arbitrary input; invalid YAML or invalid values simply fail
// validation.
func FuzzConfigYAML(f *testing.F) {
	f.Add([]byte("features:\n  webui_enabled: true\nserver:\n  web_bind: \"127.0.0.1:8080\"\n"))
	f.Add([]byte("devices:\n  - id: x\n    host: 127.0.0.1\n    port: 5900\n    virtual_port: 5910\n    enabled: true\n"))
	f.Add([]byte("connection_limits:\n  max_session_kbps: -1\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		cfg := DefaultConfig()
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return
		}
		_ = cfg.Validate()
	})
}

// FuzzTargetsYAML exercises the standalone targets.yaml parser and the device
// validation path.
func FuzzTargetsYAML(f *testing.F) {
	f.Add([]byte("targets:\n  - id: x\n    host: 127.0.0.1\n    port: 5900\n    virtual_port: 5910\n    enabled: true\n"))
	f.Add([]byte("targets:\n  - id: \"\"\n    port: -1\n"))
	f.Add([]byte("targets: []\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		var tf TargetsConfigFile
		if err := yaml.Unmarshal(data, &tf); err != nil {
			return
		}
		cfg := DefaultConfig()
		cfg.Devices = tf.Targets
		_ = cfg.Validate()
	})
}
