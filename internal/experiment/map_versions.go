package experiment

// ExpandMaps preserves the input order and materializes each requested version.
// Call after validation; the original definition is never modified.
func ExpandMaps(input []Map) []Map {
	result := []Map{}
	for _, m := range input {
		if m.Versions == nil {
			result = append(result, m)
			continue
		}
		for _, v := range m.Versions {
			copy := m
			copy.Version = v
			copy.Versions = nil
			result = append(result, copy)
		}
	}
	return result
}

// MarshalYAML writes the public versions-list syntax, including for pinned runs
// stored in reproduction metadata. JSON plans retain their singular version.
func (m Map) MarshalYAML() (any, error) {
	versions := m.Versions
	if m.Version != "" {
		versions = []string{m.Version}
	}
	return struct {
		Name     string   `yaml:"name"`
		Ref      string   `yaml:"ref,omitempty"`
		Versions []string `yaml:"versions,omitempty"`
		Source   string   `yaml:"source,omitempty"`
		SHA256   string   `yaml:"sha256,omitempty"`
	}{m.Name, m.Ref, versions, m.Source, m.SHA256}, nil
}

func (m Map) DisplayName() string {
	version := m.Version
	if m.Resolved != nil {
		version = m.Resolved.Version
	}
	if version != "" {
		return m.Name + "@" + version
	}
	return m.Name
}
