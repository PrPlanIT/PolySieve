package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// A file's map replaces the default rather than merging into it; unset fields keep defaults.
func TestLoadOverlay(t *testing.T) {
	p := filepath.Join(t.TempDir(), "polysieve.yml")
	body := `labels:
  ingress: example.com/ingress
outputs:
  healthcheck:
    prober:
      matchLabels:
        app: prober
  gatewayIngress:
    enabled: false
`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	cfg.Overlay(file)

	if cfg.Labels.Ingress != "example.com/ingress" || cfg.Labels.Probe != LabelPrefix+"probe" {
		t.Errorf("labels = %+v", cfg.Labels)
	}
	if want := map[string]string{"app": "prober"}; !reflect.DeepEqual(cfg.Outputs.Healthcheck.Prober.MatchLabels, want) {
		t.Errorf("prober = %v, want %v", cfg.Outputs.Healthcheck.Prober.MatchLabels, want)
	}
	if On(cfg.Outputs.GatewayIngress.Enabled) || !On(cfg.Outputs.Contract.Enabled) {
		t.Errorf("enabled flags wrong: %+v", cfg.Outputs)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	p := filepath.Join(t.TempDir(), "polysieve.yml")
	if err := os.WriteFile(p, []byte("lables: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Error("expected an error for an unknown field")
	}
}
