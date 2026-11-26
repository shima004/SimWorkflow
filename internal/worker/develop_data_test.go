package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"simworkflow/internal/experiment"
)

func TestDevelopDataJavaBaseAndArguments(t *testing.T) {
	app := t.TempDir()
	if err := os.Mkdir(filepath.Join(app, "config"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"launch.cfg":  "!include develop.cfg\n",
		"develop.cfg": "adf.develop.filename: config/team.json # shared defaults\n",
		"team.json":   `{"Threshold": 5, "Keep": false}`,
	} {
		if err := os.WriteFile(filepath.Join(app, "config", name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	base, err := javaDevelopFile(app)
	if err != nil {
		t.Fatal(err)
	}
	role := "fireBrigade"
	output := developDataPath(app, role)
	if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
		t.Fatal(err)
	}
	var c Config
	c.Run.Configuration.Runtime = "java"
	c.Run.Configuration.DevelopData = experiment.DevelopData{"Threshold": json.RawMessage("15")}
	if err := writeDevelopData(app, output, base, c.Run.Configuration.DevelopData); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	var merged experiment.DevelopData
	if err != nil || json.Unmarshal(data, &merged) != nil || len(merged) != 2 || string(merged["Keep"]) != "false" || string(merged["Threshold"]) != "15" {
		t.Fatalf("unexpected DevelopData JSON: %s (%v)", data, err)
	}
	for _, phase := range []string{Precompute, Simulation} {
		args := strings.Join(agentCommand(c, phase, "module.cfg", make([]int, 6), app, role), " ")
		if !strings.Contains(args, "-dev true -df "+output) {
			t.Fatalf("%s does not use the merged JSON: %s", phase, args)
		}
	}
}
