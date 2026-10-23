// Package experiment validates experiment definitions independently of services.
package experiment

import (
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const APIVersion = "simworkflow/v1alpha1"

type Experiment struct {
	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Kind       string   `yaml:"kind" json:"kind"`
	Metadata   Metadata `yaml:"metadata" json:"metadata"`
	Spec       Spec     `yaml:"spec" json:"spec"`
}

type Metadata struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

type Spec struct {
	Simulator  Container  `yaml:"simulator" json:"simulator"`
	Agents     []Agent    `yaml:"agents" json:"agents"`
	Conditions Conditions `yaml:"conditions" json:"conditions"`
	Execution  Execution  `yaml:"execution" json:"execution"`
	Outputs    Outputs    `yaml:"outputs" json:"outputs"`
}

type SimulatorParameters struct {
	Timesteps *int `yaml:"timesteps,omitempty" json:"timesteps,omitempty"`
}

type Container struct {
	Parameters *SimulatorParameters `yaml:"parameters,omitempty" json:"parameters,omitempty"`
	Image      string               `yaml:"image" json:"image"`
	Resources  Resources            `yaml:"resources" json:"resources"`
}

type Resources struct {
	CPU    CPU    `yaml:"cpu" json:"cpu"`
	Memory string `yaml:"memory" json:"memory"`
}

// CPU retains decimal precision when validating Kubernetes millicore increments.
type CPU string

func (c *CPU) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode || (n.Tag != "!!int" && n.Tag != "!!float") {
		return fmt.Errorf("line %d: resources.cpu must be a number of cores", n.Line)
	}
	*c = CPU(n.Value)
	return nil
}

func (c CPU) MilliCores() (int64, error) {
	value, ok := new(big.Rat).SetString(string(c))
	if !ok || value.Sign() <= 0 {
		return 0, errors.New("must be a positive number of cores")
	}
	value.Mul(value, big.NewRat(1000, 1))
	if !value.IsInt() || !value.Num().IsInt64() {
		return 0, errors.New("must use increments of 0.001 cores within the supported range")
	}
	return value.Num().Int64(), nil
}

type Agent struct {
	Name            string               `yaml:"name" json:"name"`
	Runtime         string               `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Image           string               `yaml:"image" json:"image"`
	Resources       Resources            `yaml:"resources" json:"resources"`
	ResourcesByRole map[string]Resources `yaml:"resourcesByRole,omitempty" json:"resourcesByRole,omitempty"`
	Modules         map[string]string    `yaml:"modules,omitempty" json:"modules,omitempty"`
	Matrix          *Matrix              `yaml:"matrix,omitempty" json:"matrix,omitempty"`
}

type Matrix struct {
	Modules map[string][]string `yaml:"modules" json:"modules"`
	Exclude []Exclusion         `yaml:"exclude,omitempty" json:"exclude,omitempty"`
}

type Exclusion struct {
	Modules map[string]string `yaml:"modules" json:"modules"`
}

type Conditions struct {
	Maps  []Map   `yaml:"maps" json:"maps"`
	Seeds []int64 `yaml:"seeds" json:"seeds"`
}

type ResolvedMap struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Source  string `json:"source"`
	SHA256  string `json:"sha256"`
}

type Map struct {
	Version  string       `yaml:"version,omitempty" json:"version,omitempty"`
	Ref      string       `yaml:"ref,omitempty" json:"ref,omitempty"`
	SHA256   string       `yaml:"sha256,omitempty" json:"sha256,omitempty"`
	Resolved *ResolvedMap `yaml:"-" json:"resolved,omitempty"`
	Name     string       `yaml:"name" json:"name"`
	Source   string       `yaml:"source,omitempty" json:"source,omitempty"`
}

type Execution struct {
	MaxParallelRuns int    `yaml:"maxParallelRuns" json:"maxParallelRuns"`
	Timeout         string `yaml:"timeout" json:"timeout"`
}

type Outputs struct {
	Collect []string `yaml:"collect" json:"collect"`
}

// ReadFile rejects unknown fields, duplicate keys, and multi-document inputs.
func ReadFile(path string) (*Experiment, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Decode(f)
}

func Decode(r io.Reader) (*Experiment, error) {
	decoder := yaml.NewDecoder(r)
	decoder.KnownFields(true)
	var e Experiment
	if err := decoder.Decode(&e); err != nil {
		return nil, fmt.Errorf("decode experiment: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("decode experiment: %w", err)
		}
		return nil, errors.New("experiment must contain exactly one YAML document")
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return &e, nil
}

var (
	namePattern   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	modulePattern = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*(\.[A-Za-z_$][A-Za-z0-9_$]*)*$`)
	memoryPattern = regexp.MustCompile(`^([1-9][0-9]*)(Mi|Gi)$`)
)

func validName(name string) bool {
	return len(name) <= 63 && namePattern.MatchString(name)
}

func (e *Experiment) Validate() error {
	var problems []error
	add := func(path, problem string) { problems = append(problems, fmt.Errorf("%s: %s", path, problem)) }
	if e.APIVersion != APIVersion {
		add("apiVersion", "must be "+APIVersion)
	}
	if e.Kind != "Experiment" {
		add("kind", "must be Experiment")
	}
	if !validName(e.Metadata.Name) {
		add("metadata.name", "must contain 1–63 lowercase letters, digits or hyphens, beginning and ending with a letter or digit")
	}
	checkContainer := func(path, image string, resources Resources) {
		if strings.TrimSpace(image) == "" || strings.ContainsAny(image, " \t\r\n") || strings.Contains(image, "://") {
			add(path+".image", "must be a container image reference without whitespace or URL scheme")
		}
		if _, err := resources.CPU.MilliCores(); err != nil {
			add(path+".resources.cpu", err.Error())
		}
		if _, err := resources.MemoryBytes(); err != nil {
			add(path+".resources.memory", err.Error())
		}
	}
	checkContainer("spec.simulator", e.Spec.Simulator.Image, e.Spec.Simulator.Resources)
	if p := e.Spec.Simulator.Parameters; p != nil && p.Timesteps != nil && (*p.Timesteps <= 0 || int64(*p.Timesteps) > 2147483647) {
		add("spec.simulator.parameters.timesteps", "must be an integer between 1 and 2147483647")
	}
	if len(e.Spec.Agents) == 0 {
		add("spec.agents", "at least one agent is required")
	}
	seenAgents := map[string]bool{}
	for i, a := range e.Spec.Agents {
		path := fmt.Sprintf("spec.agents[%d]", i)
		if a.Runtime != "" && a.Runtime != "java" && a.Runtime != "python" {
			add(path+".runtime", "must be java or python")
		}
		if !validName(a.Name) || seenAgents[a.Name] {
			add(path+".name", "must be a unique name of 1–63 lowercase letters, digits or hyphens, beginning and ending with a letter or digit")
		}
		seenAgents[a.Name] = true
		checkContainer(path, a.Image, a.Resources)
		for _, role := range sortedKeys(a.ResourcesByRole) {
			rolePath := path + ".resourcesByRole." + role
			if !ValidRole(role) {
				add(rolePath, "must be fireBrigade, policeForce or ambulanceTeam; centres share their corresponding role")
			}
			resources := a.ResourcesByRole[role]
			if _, err := resources.CPU.MilliCores(); err != nil {
				add(rolePath+".cpu", err.Error())
			}
			if _, err := resources.MemoryBytes(); err != nil {
				add(rolePath+".memory", err.Error())
			}
		}
		for _, key := range sortedKeys(a.Modules) {
			if !modulePattern.MatchString(key) || !modulePattern.MatchString(a.Modules[key]) {
				add(path+".modules."+key, "key and implementation must be dot-separated identifiers")
			}
		}
		if a.Matrix == nil {
			continue
		}
		if len(a.Matrix.Modules) == 0 {
			add(path+".matrix.modules", "at least one module axis is required")
		}
		for _, key := range sortedKeys(a.Matrix.Modules) {
			values := a.Matrix.Modules[key]
			if !modulePattern.MatchString(key) {
				add(path+".matrix.modules", "module keys must be dot-separated identifiers")
			}
			if _, exists := a.Modules[key]; exists {
				add(path+".matrix.modules."+key, "cannot also appear in fixed modules")
			}
			if len(values) == 0 {
				add(path+".matrix.modules."+key, "at least one candidate is required")
			}
			seen := map[string]bool{}
			for _, value := range values {
				if !modulePattern.MatchString(value) || seen[value] {
					add(path+".matrix.modules."+key, "candidates must be unique dot-separated identifiers")
				}
				seen[value] = true
			}
		}
		for j, exclusion := range a.Matrix.Exclude {
			excludePath := fmt.Sprintf("%s.matrix.exclude[%d].modules", path, j)
			if len(exclusion.Modules) == 0 {
				add(excludePath, "at least one module is required")
			}
			for _, key := range sortedKeys(exclusion.Modules) {
				if !slices.Contains(a.Matrix.Modules[key], exclusion.Modules[key]) {
					add(excludePath+"."+key, "must reference an existing matrix axis and candidate")
				}
			}
		}
	}
	if len(e.Spec.Conditions.Maps) == 0 {
		add("spec.conditions.maps", "at least one map is required")
	}
	seenMaps := map[string]bool{}
	for i, m := range e.Spec.Conditions.Maps {
		path := fmt.Sprintf("spec.conditions.maps[%d]", i)
		if !validName(m.Name) || seenMaps[m.Name] {
			add(path+".name", "must be a unique name of 1–63 lowercase letters, digits or hyphens, beginning and ending with a letter or digit")
		}
		seenMaps[m.Name] = true
		if m.Version != "" {
			if !ValidMapVersion(m.Version) {
				add(path+".version", "must be a version identifier or latest")
			}
			if m.Source != "" || m.SHA256 != "" {
				add(path, "specify version or source, not both")
			}
			if m.Ref != "" && !validName(m.Ref) {
				add(path+".ref", "must be a registered map name")
			}
		} else {
			if m.Ref != "" {
				add(path+".ref", "requires version")
			}
			u, err := url.Parse(m.Source)
			if err != nil || u.Scheme != "s3" || u.Host == "" || u.User != nil || strings.TrimPrefix(u.Path, "/") == "" || u.RawQuery != "" || u.Fragment != "" {
				add(path+".source", "must be an s3://bucket/key reference without credentials, query or fragment")
			}
			if m.SHA256 != "" && !ValidSHA256(m.SHA256) {
				add(path+".sha256", "must be 64 lowercase hexadecimal characters")
			}
		}
		if r := m.Resolved; r != nil && (m.Version == "" || r.Name != m.RegistryName() || !ValidMapVersion(r.Version) || r.Version == "latest" || !ValidSHA256(r.SHA256) || !strings.HasPrefix(r.Source, "s3://")) {
			add(path, "invalid resolved map")
		}
	}
	if len(e.Spec.Conditions.Seeds) == 0 {
		add("spec.conditions.seeds", "at least one integer seed is required")
	}
	seenSeeds := map[int64]bool{}
	for _, seed := range e.Spec.Conditions.Seeds {
		if seenSeeds[seed] {
			add("spec.conditions.seeds", "duplicate seeds are not allowed")
		}
		seenSeeds[seed] = true
	}
	if e.Spec.Execution.MaxParallelRuns <= 0 {
		add("spec.execution.maxParallelRuns", "must be a positive integer")
	}
	if timeout, err := time.ParseDuration(e.Spec.Execution.Timeout); err != nil || timeout <= 0 || timeout%time.Second != 0 {
		add("spec.execution.timeout", "must be a positive duration in whole seconds, such as 30m")
	}
	if len(e.Spec.Outputs.Collect) == 0 {
		add("spec.outputs.collect", "at least one output is required")
	}
	seenOutputs := map[string]bool{}
	for _, output := range e.Spec.Outputs.Collect {
		if !slices.Contains([]string{"score", "simulatorLogs", "agentLogs"}, output) || seenOutputs[output] {
			add("spec.outputs.collect", "must contain unique values from score, simulatorLogs, agentLogs")
		}
		seenOutputs[output] = true
	}
	return errors.Join(problems...)
}

func (r Resources) MemoryBytes() (int64, error) {
	parts := memoryPattern.FindStringSubmatch(r.Memory)
	if parts == nil {
		return 0, errors.New("must be a positive integer with Mi or Gi suffix")
	}
	value, _ := new(big.Int).SetString(parts[1], 10)
	shift := uint(20)
	if parts[2] == "Gi" {
		shift = 30
	}
	value.Lsh(value, shift)
	if !value.IsInt64() {
		return 0, errors.New("exceeds the supported memory range")
	}
	return value.Int64(), nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
