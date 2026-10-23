package moduleconfig

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestRenderJava(t *testing.T) {
	base := "## Original comment\r\nSampleRoadDetector.PathPlanning : team.Default\r\nSampleRoadDetector.Clustering : team.Clustering\r\nVisualDebug : true\r\n"
	got, err := Render([]byte(base), "java", map[string]string{"SampleRoadDetector.PathPlanning": "team.Custom"})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(base, "team.Default", "team.Custom", 1)
	if string(got) != want {
		t.Fatalf("only the selected implementation should change:\n%s", got)
	}
}

func TestRenderPython(t *testing.T) {
	base := "# Original comment\nSampleRoadDetector:\n  PathPlanning: team.Default # implementation\n  Clustering: team.Clustering\nVisualDebug: true\nOtherSettings: [one, two]\n"
	got, err := Render([]byte(base), "python", map[string]string{"SampleRoadDetector.PathPlanning": "team.Custom"})
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Road struct {
			PathPlanning string `yaml:"PathPlanning"`
			Clustering   string `yaml:"Clustering"`
		} `yaml:"SampleRoadDetector"`
		Debug bool     `yaml:"VisualDebug"`
		Other []string `yaml:"OtherSettings"`
	}
	if err := yaml.Unmarshal(got, &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Road.PathPlanning != "team.Custom" || settings.Road.Clustering != "team.Clustering" || !settings.Debug || len(settings.Other) != 2 {
		t.Fatalf("unselected settings must be preserved: %s", got)
	}
	if !strings.Contains(string(got), "# Original comment") || !strings.Contains(string(got), "# implementation") {
		t.Fatal("comments must be retained")
	}
	flat, err := Render([]byte("SampleRoadDetector.PathPlanning: team.Default\n"), "python", map[string]string{"SampleRoadDetector.PathPlanning": "team.Custom"})
	if err != nil || !strings.Contains(string(flat), "team.Custom") {
		t.Fatalf("flat dotted keys must be supported: %s, %v", flat, err)
	}
}

func TestRejectAmbiguousOrUnknownOverrides(t *testing.T) {
	tests := []struct{ format, base, key, value string }{
		{"java", "A : team.Default\n", "Missing", "team.Custom"},
		{"java", "A : team.Default\nA : team.Other\n", "A", "team.Custom"},
		{"java", "not an assignment\n", "A", "team.Custom"},
		{"java", "A : team.Default\n", "A", "team.Custom\nInjected : team.Other"},
		{"python", "A: team.Default\n", "Missing", "team.Custom"},
		{"python", "A: team.Default\nA: team.Other\n", "A", "team.Custom"},
		{"python", "A: {B: team.Default}\nA.B: team.Other\n", "A.B", "team.Custom"},
		{"python", "A: &alias team.Default\nB: *alias\n", "A", "team.Custom"},
		{"python", "A: true\n", "A", "team.Custom"},
		{"python", "A: team.Default\n---\nB: team.Other\n", "A", "team.Custom"},
		{"python", "[one, two]\n", "A", "team.Custom"},
		{"other", "A : team.Default\n", "A", "team.Custom"},
	}
	for _, tc := range tests {
		if _, err := Render([]byte(tc.base), tc.format, map[string]string{tc.key: tc.value}); err == nil {
			t.Fatalf("expected rejection: %+v", tc)
		}
	}
}
