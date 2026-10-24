package experiment

import (
	"fmt"
	"regexp"
	"slices"
)

var groupNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,62}$`)

// Build the catalog for module-based exclusions while enforcing a single owner
// for every module key. A group's options must replace the same set of keys.
func validateGroups(agent Agent, path string, add func(string, string)) map[string][]string {
	catalog := map[string][]string{}
	for key, values := range agent.Matrix.Modules {
		catalog[key] = slices.Clone(values)
	}
	owners := map[string]string{}
	for _, group := range sortedKeys(agent.Matrix.Groups) {
		options := agent.Matrix.Groups[group]
		groupPath := path + ".matrix.groups." + group
		if !groupNamePattern.MatchString(group) {
			add(groupPath, "group names must contain 1-63 ASCII letters, digits, underscores or hyphens and start with a letter")
		}
		if len(options) == 0 {
			add(groupPath, "at least one candidate is required")
		}
		seenNames, seenContents := map[string]bool{}, map[string]bool{}
		var expected []string
		for index, option := range options {
			optionPath := fmt.Sprintf("%s[%d]", groupPath, index)
			if !groupNamePattern.MatchString(option.Name) || seenNames[option.Name] {
				add(optionPath+".name", "candidate names must be unique within the group, contain 1-63 ASCII letters, digits, underscores or hyphens and start with a letter")
			}
			seenNames[option.Name] = true
			keys := sortedKeys(option.Modules)
			if len(keys) == 0 {
				add(optionPath+".modules", "at least one module is required")
			}
			if index == 0 {
				expected = keys
			} else if !slices.Equal(expected, keys) {
				add(optionPath+".modules", "all candidates in a group must define the same module keys")
			}
			content := digest(option.Modules)
			if seenContents[content] {
				add(optionPath+".modules", "candidates must define distinct module sets")
			}
			seenContents[content] = true
			for _, key := range keys {
				value := option.Modules[key]
				if !modulePattern.MatchString(key) || !modulePattern.MatchString(value) {
					add(optionPath+".modules."+key, "key and implementation must be dot-separated identifiers")
				}
				if _, exists := agent.Modules[key]; exists {
					add(optionPath+".modules."+key, "cannot also appear in fixed modules")
				}
				if _, exists := agent.Matrix.Modules[key]; exists {
					add(optionPath+".modules."+key, "cannot also appear in matrix.modules")
				}
				if owner, exists := owners[key]; exists && owner != group {
					add(optionPath+".modules."+key, "cannot appear in more than one group")
				}
				owners[key] = group
				catalog[key] = append(catalog[key], value)
			}
		}
	}
	return catalog
}
