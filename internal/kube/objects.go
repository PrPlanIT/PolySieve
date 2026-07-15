// Package kube parses a rendered Kubernetes manifest stream into the minimal set of
// typed objects PolySieve reasons about — Gateway API HTTPRoutes, Services, EndpointSlices,
// and workload pod templates. It intentionally does NOT depend on the full k8s or
// gateway-api Go types: the derivation touches a handful of fields, and lightweight structs
// keep parsing predictable and the dependency surface tiny.
package kube

import (
	"bytes"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// IntOrString models a value that may be an integer or a string in YAML — e.g. a Service
// port's targetPort (a number like 8443 or a named port like "http"). Absent is neither.
type IntOrString struct {
	Present bool
	IsInt   bool
	Int     int
	Str     string
}

// UnmarshalYAML accepts a scalar int or string.
func (v *IntOrString) UnmarshalYAML(node *yaml.Node) error {
	v.Present = true
	var i int
	if err := node.Decode(&i); err == nil {
		v.IsInt = true
		v.Int = i
		return nil
	}
	var s string
	if err := node.Decode(&s); err == nil {
		v.Str = s
		return nil
	}
	return fmt.Errorf("value %q is neither int nor string", node.Value)
}

// ObjectMeta is the shared metadata subset.
type ObjectMeta struct {
	Name        string            `yaml:"name"`
	Namespace   string            `yaml:"namespace"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
}

// ── HTTPRoute ────────────────────────────────────────────────────────────────

type HTTPRoute struct {
	Metadata ObjectMeta `yaml:"metadata"`
	Spec     struct {
		ParentRefs  []ParentRef `yaml:"parentRefs"`
		Rules       []struct {
			BackendRefs []BackendRef `yaml:"backendRefs"`
		} `yaml:"rules"`
	} `yaml:"spec"`
}

type ParentRef struct {
	Name string `yaml:"name"`
}

type BackendRef struct {
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"` // optional; defaults to the route's namespace
	Port      *int   `yaml:"port"`      // pointer: distinguishes absent from 0
}

// ── Service ──────────────────────────────────────────────────────────────────

type Service struct {
	Metadata ObjectMeta `yaml:"metadata"`
	Spec     struct {
		Selector map[string]string `yaml:"selector"`
		Ports    []ServicePort     `yaml:"ports"`
	} `yaml:"spec"`
}

type ServicePort struct {
	Name       string      `yaml:"name"`
	Port       int         `yaml:"port"`
	TargetPort IntOrString `yaml:"targetPort"`
}

// ── EndpointSlice ────────────────────────────────────────────────────────────

type EndpointSlice struct {
	Metadata ObjectMeta      `yaml:"metadata"`
	Ports    []EndpointPort  `yaml:"ports"`
}

type EndpointPort struct {
	Name string `yaml:"name"`
	Port int    `yaml:"port"`
}

// ServiceName is the service this slice backs (label kubernetes.io/service-name).
func (e EndpointSlice) ServiceName() string {
	return e.Metadata.Labels["kubernetes.io/service-name"]
}

// ── Workload (Deployment / StatefulSet / DaemonSet) ──────────────────────────
// Only the pod template is relevant: its labels (e.g. the probe label) and container ports.

type Workload struct {
	Metadata ObjectMeta `yaml:"metadata"`
	Spec     struct {
		Template struct {
			Metadata struct {
				Labels map[string]string `yaml:"labels"`
			} `yaml:"metadata"`
			Spec struct {
				Containers []struct {
					Ports []ContainerPort `yaml:"ports"`
				} `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

type ContainerPort struct {
	Name          string `yaml:"name"`
	ContainerPort int    `yaml:"containerPort"`
	Protocol      string `yaml:"protocol"`
}

// ── Flux Helm objects (for optional HelmRelease rendering) ───────────────────
// Enough of the Flux CRDs to locate a chart and its values; rendering happens in the helm
// package by shelling out to `helm template`.

type HelmRelease struct {
	Metadata ObjectMeta `yaml:"metadata"`
	Spec     struct {
		ReleaseName     string `yaml:"releaseName"`
		TargetNamespace string `yaml:"targetNamespace"`
		Chart           struct {
			Spec struct {
				Chart     string    `yaml:"chart"`
				Version   string    `yaml:"version"`
				SourceRef SourceRef `yaml:"sourceRef"`
			} `yaml:"spec"`
		} `yaml:"chart"`
		ChartRef SourceRef `yaml:"chartRef"`
		Values   yaml.Node `yaml:"values"`
	} `yaml:"spec"`
}

type SourceRef struct {
	Kind      string `yaml:"kind"`
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
}

type HelmRepository struct {
	Metadata ObjectMeta `yaml:"metadata"`
	Spec     struct {
		URL  string `yaml:"url"`
		Type string `yaml:"type"` // "oci" for an OCI-hosted Helm repo, else a classic index repo
	} `yaml:"spec"`
}

type OCIRepository struct {
	Metadata ObjectMeta `yaml:"metadata"`
	Spec     struct {
		URL string `yaml:"url"` // oci://host/path/chart
		Ref struct {
			Tag string `yaml:"tag"`
		} `yaml:"ref"`
	} `yaml:"spec"`
}

// ── Object set ───────────────────────────────────────────────────────────────

// Objects is everything PolySieve extracted from a rendered manifest stream.
type Objects struct {
	HTTPRoutes       []HTTPRoute
	Services         []Service
	EndpointSlices   []EndpointSlice
	Workloads        []Workload
	HelmReleases     []HelmRelease
	HelmRepositories []HelmRepository
	OCIRepositories  []OCIRepository
}

// typeMeta is used to route each YAML document to its decoder.
type typeMeta struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
}

// Parse decodes a multi-document manifest stream (as produced by `kustomize build`, or a
// `kind: List` from `kubectl get -o yaml`) into the typed object set. Documents of kinds
// PolySieve does not care about are skipped. Parse appends to dst so callers can accumulate
// across multiple rendered roots and, optionally, live-cluster objects.
func Parse(dst *Objects, stream []byte) error {
	dec := yaml.NewDecoder(bytes.NewReader(stream))
	for {
		var raw yaml.Node
		if err := dec.Decode(&raw); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("decoding manifest stream: %w", err)
		}
		if raw.Kind == 0 || (raw.Kind == yaml.DocumentNode && len(raw.Content) == 0) {
			continue // empty document
		}
		if err := dispatch(dst, &raw); err != nil {
			return err
		}
	}
}

// dispatch routes a single decoded node to its typed slice, recursing into `kind: List` items.
func dispatch(dst *Objects, node *yaml.Node) error {
	var tm typeMeta
	if err := node.Decode(&tm); err != nil {
		return nil // not a k8s object we can classify; skip
	}
	switch tm.Kind {
	case "HTTPRoute":
		var o HTTPRoute
		if err := node.Decode(&o); err != nil {
			return fmt.Errorf("decoding HTTPRoute: %w", err)
		}
		dst.HTTPRoutes = append(dst.HTTPRoutes, o)
	case "Service":
		var o Service
		if err := node.Decode(&o); err != nil {
			return fmt.Errorf("decoding Service: %w", err)
		}
		dst.Services = append(dst.Services, o)
	case "EndpointSlice":
		var o EndpointSlice
		if err := node.Decode(&o); err != nil {
			return fmt.Errorf("decoding EndpointSlice: %w", err)
		}
		dst.EndpointSlices = append(dst.EndpointSlices, o)
	case "Deployment", "StatefulSet", "DaemonSet":
		var o Workload
		if err := node.Decode(&o); err != nil {
			return fmt.Errorf("decoding %s: %w", tm.Kind, err)
		}
		dst.Workloads = append(dst.Workloads, o)
	case "HelmRelease":
		var o HelmRelease
		if err := node.Decode(&o); err != nil {
			return fmt.Errorf("decoding HelmRelease: %w", err)
		}
		dst.HelmReleases = append(dst.HelmReleases, o)
	case "HelmRepository":
		var o HelmRepository
		if err := node.Decode(&o); err != nil {
			return fmt.Errorf("decoding HelmRepository: %w", err)
		}
		dst.HelmRepositories = append(dst.HelmRepositories, o)
	case "OCIRepository":
		var o OCIRepository
		if err := node.Decode(&o); err != nil {
			return fmt.Errorf("decoding OCIRepository: %w", err)
		}
		dst.OCIRepositories = append(dst.OCIRepositories, o)
	case "List":
		var list struct {
			Items []yaml.Node `yaml:"items"`
		}
		if err := node.Decode(&list); err != nil {
			return fmt.Errorf("decoding List: %w", err)
		}
		for i := range list.Items {
			if err := dispatch(dst, &list.Items[i]); err != nil {
				return err
			}
		}
	}
	return nil
}
