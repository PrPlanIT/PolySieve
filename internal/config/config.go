// Package config holds PolySieve's settings. Every field has a default derived from upstream
// convention or from the repo itself; a config file (--config) or flags override them.
package config

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// LabelPrefix is the prefix of PolySieve's default label keys.
const LabelPrefix = "polysieve.prplanit.com/"

type Config struct {
	// Roots are repo-relative kustomize build directories. Empty: derived from the repo's Flux
	// Kustomizations.
	Roots    []string `yaml:"roots"`
	Labels   Labels   `yaml:"labels"`
	Gateways Gateways `yaml:"gateways"`
	Outputs  Outputs  `yaml:"outputs"`
}

type Labels struct {
	Ingress string `yaml:"ingress"` // pod label (value "true") marking a backend reachable from ingress gateways
	Probe   string `yaml:"probe"`   // pod label (value "true") marking a pod the health prober may reach
}

type Gateways struct {
	// Selector matches Gateway infrastructure labels. Empty: every Istio-managed Gateway.
	Selector map[string]string `yaml:"selector"`
	// InstanceLabel is the infrastructure label holding the gateway's short instance name.
	InstanceLabel string `yaml:"instanceLabel"`
	// PodSelector selects the gateway pods in each gateway namespace as the contract's source.
	// Empty: each gateway's pods by gateway.networking.k8s.io/gateway-name.
	PodSelector map[string]string `yaml:"podSelector"`
}

type Outputs struct {
	Contract       Output      `yaml:"contract"`
	Healthcheck    Healthcheck `yaml:"healthcheck"`
	GatewayIngress DirOutput   `yaml:"gatewayIngress"`
}

// Output is one generated policy file. Empty Path: where the repo already holds the generated
// policy of that name, else under policies/.
type Output struct {
	Enabled *bool  `yaml:"enabled"`
	Name    string `yaml:"name"`
	Path    string `yaml:"path"`
}

type Healthcheck struct {
	Output             `yaml:",inline"`
	Prober             Selector           `yaml:"prober"`
	ServiceAnnotations ServiceAnnotations `yaml:"serviceAnnotations"`
}

type Selector struct {
	MatchLabels map[string]string `yaml:"matchLabels"`
}

// ServiceAnnotations are the Service annotations that opt a Service into health probing.
type ServiceAnnotations struct {
	Enabled  string `yaml:"enabled"`  // value "true" opts in
	Endpoint string `yaml:"endpoint"` // any value opts in
}

// DirOutput is a per-namespace policy set: <Dir>/<namespace>/<Name>-<instance>.yaml.
type DirOutput struct {
	Enabled *bool  `yaml:"enabled"`
	Name    string `yaml:"name"`
	Dir     string `yaml:"dir"`
}

// On reports whether an output is enabled (default on).
func On(b *bool) bool { return b == nil || *b }

// Default returns the settings PolySieve uses when nothing overrides them.
func Default() Config {
	return Config{
		Labels: Labels{
			Ingress: LabelPrefix + "ingress",
			Probe:   LabelPrefix + "probe",
		},
		Gateways: Gateways{InstanceLabel: LabelPrefix + "ingress-instance"},
		Outputs: Outputs{
			Contract: Output{Name: "contract-ingress-backend"},
			Healthcheck: Healthcheck{
				Output: Output{Name: "allow-gatus-healthcheck"},
				Prober: Selector{MatchLabels: map[string]string{"app.kubernetes.io/name": "gatus"}},
				ServiceAnnotations: ServiceAnnotations{
					Enabled:  "gatus.home-operations.com/enabled",
					Endpoint: "gatus.home-operations.com/endpoint",
				},
			},
			GatewayIngress: DirOutput{Name: "allow-gateway-ingress"},
		},
	}
}

// Load reads the config file at path. Unknown fields are an error. The result holds only what
// the file sets; Overlay it onto Default.
func Load(path string) (Config, error) {
	var cfg Config
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Overlay sets every field of c that o sets. A map or list replaces, never merges.
func (c *Config) Overlay(o Config) {
	if len(o.Roots) > 0 {
		c.Roots = o.Roots
	}
	str(&c.Labels.Ingress, o.Labels.Ingress)
	str(&c.Labels.Probe, o.Labels.Probe)
	mp(&c.Gateways.Selector, o.Gateways.Selector)
	str(&c.Gateways.InstanceLabel, o.Gateways.InstanceLabel)
	mp(&c.Gateways.PodSelector, o.Gateways.PodSelector)
	c.Outputs.Contract.overlay(o.Outputs.Contract)
	c.Outputs.Healthcheck.Output.overlay(o.Outputs.Healthcheck.Output)
	mp(&c.Outputs.Healthcheck.Prober.MatchLabels, o.Outputs.Healthcheck.Prober.MatchLabels)
	str(&c.Outputs.Healthcheck.ServiceAnnotations.Enabled, o.Outputs.Healthcheck.ServiceAnnotations.Enabled)
	str(&c.Outputs.Healthcheck.ServiceAnnotations.Endpoint, o.Outputs.Healthcheck.ServiceAnnotations.Endpoint)
	if o.Outputs.GatewayIngress.Enabled != nil {
		c.Outputs.GatewayIngress.Enabled = o.Outputs.GatewayIngress.Enabled
	}
	str(&c.Outputs.GatewayIngress.Name, o.Outputs.GatewayIngress.Name)
	str(&c.Outputs.GatewayIngress.Dir, o.Outputs.GatewayIngress.Dir)
}

func (c *Output) overlay(o Output) {
	if o.Enabled != nil {
		c.Enabled = o.Enabled
	}
	str(&c.Name, o.Name)
	str(&c.Path, o.Path)
}

func str(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

func mp(dst *map[string]string, v map[string]string) {
	if len(v) > 0 {
		*dst = v
	}
}
