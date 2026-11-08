package policy

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/PrPlanIT/PolySieve/internal/config"
)

// Default locations for outputs the repo doesn't hold yet.
const (
	defaultCiliumDir = "policies/cilium"
	defaultIstioDir  = "policies/istio"
)

// Locate fills each enabled output's empty path from where the repo already holds the generated
// policy of that name (a file starting with the derived marker), else the default location.
func Locate(repoDir string, cfg *config.Config) error {
	found, err := scanGenerated(repoDir)
	if err != nil {
		return err
	}
	for _, o := range []*config.Output{&cfg.Outputs.Contract, &cfg.Outputs.Healthcheck.Output} {
		if o.Path != "" || !config.On(o.Enabled) {
			continue
		}
		paths := found["CiliumClusterwideNetworkPolicy/"+o.Name]
		switch len(paths) {
		case 0:
			o.Path = defaultCiliumDir + "/ccnp-" + o.Name + ".yaml"
		case 1:
			o.Path = paths[0]
		default:
			return fmt.Errorf("generated policy %s found in several files %v; set its path", o.Name, paths)
		}
	}

	gi := &cfg.Outputs.GatewayIngress
	if gi.Dir != "" || !config.On(gi.Enabled) {
		return nil
	}
	dirs := map[string]bool{}
	for k, paths := range found {
		if !strings.HasPrefix(k, "AuthorizationPolicy/"+gi.Name+"-") {
			continue
		}
		for _, p := range paths {
			dirs[filepath.Dir(filepath.Dir(p))] = true // <dir>/<namespace>/<file>
		}
	}
	switch len(dirs) {
	case 0:
		gi.Dir = defaultIstioDir
	case 1:
		for d := range dirs {
			gi.Dir = d
		}
	default:
		list := make([]string, 0, len(dirs))
		for d := range dirs {
			list = append(list, d)
		}
		sort.Strings(list)
		return fmt.Errorf("generated %s policies found under several directories %v; set its dir", gi.Name, list)
	}
	return nil
}

// scanGenerated maps "Kind/name" to the repo-relative paths of generated files, outside
// dot-directories.
func scanGenerated(repoDir string) (map[string][]string, error) {
	found := map[string][]string{}
	marker := []byte(derivedMarker)
	err := filepath.WalkDir(repoDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != repoDir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(path); ext != ".yaml" && ext != ".yml" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil || !bytes.HasPrefix(b, marker) {
			return nil
		}
		var meta struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
		}
		if yaml.Unmarshal(b, &meta) != nil {
			return nil
		}
		rel, err := filepath.Rel(repoDir, path)
		if err != nil {
			return err
		}
		k := meta.Kind + "/" + meta.Metadata.Name
		found[k] = append(found[k], filepath.ToSlash(rel))
		return nil
	})
	return found, err
}
