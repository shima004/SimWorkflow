package experiment

import "regexp"

var mapVersionPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$`)
var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func ValidMapName(name string) bool { return validName(name) }
func ValidMapVersion(version string) bool {
	return len(version) <= 63 && mapVersionPattern.MatchString(version)
}
func ValidSHA256(value string) bool { return sha256Pattern.MatchString(value) }
func (m Map) RegistryName() string {
	if m.Ref != "" {
		return m.Ref
	}
	return m.Name
}
func (m Map) Location() string {
	if m.Resolved != nil {
		return m.Resolved.Source
	}
	return m.Source
}
func (m Map) ExpectedSHA256() string {
	if m.Resolved != nil {
		return m.Resolved.SHA256
	}
	return m.SHA256
}
