// Package manifest loads, validates, and resolves composefile.yaml.
//
// A manifest describes one deployment set: a single SSH target and a set of
// Docker Compose stacks deployed in order.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// DefaultRemoteRoot is the fallback deployment root when defaults.remote_root
	// is unset. A leading "~" is expanded against the remote user's home at
	// runtime.
	DefaultRemoteRoot = "~/.local/share/composefile"

	// DefaultHealthTimeout is the fallback up --wait-timeout when a stack does
	// not set health_timeout.
	DefaultHealthTimeout = 120 * time.Second

	// DefaultPrune is the fallback pruning behavior.
	DefaultPrune = PruneNone
)

// ComposeFileCandidates are detected by `init` when generating a manifest.
var ComposeFileCandidates = []string{
	"compose.yaml",
	"compose.yml",
	"docker-compose.yml",
	"docker-compose.yaml",
}

// PruneScope controls post-success Docker pruning.
type PruneScope string

const (
	PruneNone   PruneScope = "none"
	PruneImages PruneScope = "images"
	PruneSystem PruneScope = "system"
)

// UnmarshalYAML validates prune scopes strictly and rejects unknown values.
func (p *PruneScope) UnmarshalYAML(node *yaml.Node) error {
	var raw string
	if err := node.Decode(&raw); err != nil {
		return err
	}
	switch PruneScope(raw) {
	case PruneNone, PruneImages, PruneSystem:
		*p = PruneScope(raw)
		return nil
	default:
		return fmt.Errorf("invalid prune scope %q (allowed: none, images, system)", raw)
	}
}

// Defaults holds deployment defaults shared by every stack.
type Defaults struct {
	RemoteRoot    string     `yaml:"remote_root"`
	HealthTimeout string     `yaml:"health_timeout"`
	Prune         PruneScope `yaml:"prune"`
}

// Stack is one Docker Compose project within a deployment set.
type Stack struct {
	Name          string   `yaml:"name"`
	Source        string   `yaml:"source"`
	Compose       []string `yaml:"compose"`
	HealthTimeout string   `yaml:"health_timeout"`

	// Resolved at Load time.
	SourceAbs      string        `yaml:"-"`
	ComposeAbs     []string      `yaml:"-"`
	HealthTimeoutD time.Duration `yaml:"-"`
}

// Manifest is the decoded composefile.yaml.
type Manifest struct {
	Name     string   `yaml:"name"`
	Target   string   `yaml:"target"`
	Defaults Defaults `yaml:"defaults"`
	Stacks   []Stack  `yaml:"stacks"`

	// Dir is the absolute directory containing composefile.yaml.
	Dir string `yaml:"-"`
}

// bundleNameRe validates deployment-set names used in bundle filenames.
var bundleNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidateBundleName reports whether name is safe to embed in a bundle filename.
func ValidateBundleName(name string) bool { return bundleNameRe.MatchString(name) }

// Load reads, strictly decodes, defaults, and resolves composefile.yaml in dir.
func Load(dir string) (*Manifest, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(abs, "composefile.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	m.Dir = abs

	if err := m.applyDefaults(); err != nil {
		return nil, err
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	if err := m.resolveStacks(); err != nil {
		return nil, err
	}
	return &m, nil
}

// applyDefaults fills unset defaults before validation.
func (m *Manifest) applyDefaults() error {
	if m.Defaults.RemoteRoot == "" {
		m.Defaults.RemoteRoot = DefaultRemoteRoot
	}
	if m.Defaults.Prune == "" {
		m.Defaults.Prune = DefaultPrune
	}

	dh := DefaultHealthTimeout
	if m.Defaults.HealthTimeout != "" {
		d, err := time.ParseDuration(m.Defaults.HealthTimeout)
		if err != nil {
			return fmt.Errorf("manifest: defaults.health_timeout %q: %w", m.Defaults.HealthTimeout, err)
		}
		if d <= 0 {
			return fmt.Errorf("manifest: defaults.health_timeout must be positive")
		}
		dh = d
	}

	for i := range m.Stacks {
		s := &m.Stacks[i]
		s.HealthTimeoutD = dh
		if s.HealthTimeout != "" {
			d, err := time.ParseDuration(s.HealthTimeout)
			if err != nil {
				return fmt.Errorf("manifest: stack %q health_timeout %q: %w", s.Name, s.HealthTimeout, err)
			}
			if d <= 0 {
				return fmt.Errorf("manifest: stack %q health_timeout must be positive", s.Name)
			}
			s.HealthTimeoutD = d
		}
	}
	return nil
}

// validate runs structural validation after defaults are applied.
func (m *Manifest) validate() error {
	if m.Name == "" {
		return errors.New("manifest: name is required")
	}
	if !bundleNameRe.MatchString(m.Name) {
		return fmt.Errorf("manifest: name %q must match %s", m.Name, bundleNameRe)
	}
	if m.Target == "" {
		return errors.New("manifest: target is required")
	}
	if len(m.Stacks) == 0 {
		return errors.New("manifest: at least one stack is required")
	}

	seen := make(map[string]bool, len(m.Stacks))
	for _, s := range m.Stacks {
		if s.Name == "" {
			return errors.New("manifest: every stack requires a name")
		}
		if seen[s.Name] {
			return fmt.Errorf("manifest: duplicate stack name %q", s.Name)
		}
		seen[s.Name] = true

		if s.Source == "" {
			return fmt.Errorf("manifest: stack %q requires a source", s.Name)
		}
		if len(s.Compose) == 0 {
			return fmt.Errorf("manifest: stack %q requires at least one compose file", s.Name)
		}
	}
	return nil
}

// resolveStacks resolves source and compose paths relative to the manifest dir.
func (m *Manifest) resolveStacks() error {
	for i := range m.Stacks {
		s := &m.Stacks[i]

		if !filepath.IsAbs(s.Source) {
			s.Source = filepath.Join(m.Dir, filepath.FromSlash(s.Source))
		}
		s.SourceAbs = filepath.Clean(s.Source)

		info, err := os.Stat(s.SourceAbs)
		if err != nil {
			return fmt.Errorf("manifest: stack %q source: %w", s.Name, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("manifest: stack %q source %q is not a directory", s.Name, s.SourceAbs)
		}

		for _, c := range s.Compose {
			p := c
			if !filepath.IsAbs(p) {
				p = filepath.Join(s.SourceAbs, filepath.FromSlash(c))
			}
			p = filepath.Clean(p)

			rel, err := filepath.Rel(s.SourceAbs, p)
			if err != nil || rel == ".." || len(rel) > 2 && rel[:3] == ".."+string(filepath.Separator) {
				return fmt.Errorf("manifest: stack %q compose file %q must resolve inside source %q", s.Name, c, s.SourceAbs)
			}
			if _, err := os.Stat(p); err != nil {
				return fmt.Errorf("manifest: stack %q compose file: %w", s.Name, err)
			}
			s.ComposeAbs = append(s.ComposeAbs, p)
		}
	}
	return nil
}
