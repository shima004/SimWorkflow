package maparchive

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{"map/map.gml": "<map><building id=\"1\"/></map>", "map/scenario.xml": "<scenario><firebrigade/><firestation/><policeforce/><policeoffice/><ambulanceteam/><ambulancecentre/></scenario>", "config/kernel.cfg": "kernel.timesteps: 300\n", "config/common.cfg": "random.seed: 1\n"}
	for name, data := range files {
		path := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(path), 0700)
		os.WriteFile(path, []byte(data), 0600)
	}
	return root
}

func TestDeterministicPackageAndInspection(t *testing.T) {
	root := fixture(t)
	os.WriteFile(filepath.Join(root, ".env"), []byte("must-not-be-uploaded"), 0600)
	first := filepath.Join(t.TempDir(), "first.zip")
	second := filepath.Join(t.TempDir(), "second.zip")
	if err := PackageDirectory(root, first); err != nil {
		t.Fatal(err)
	}
	if err := PackageDirectory(root, second); err != nil {
		t.Fatal(err)
	}
	a, err := Inspect(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Inspect(second)
	if err != nil {
		t.Fatal(err)
	}
	if a.SHA256 != b.SHA256 || a.SizeBytes <= 0 || a.Agents.FireStation != 1 || a.Agents.PoliceOffice != 1 || a.Agents.AmbulanceCentre != 1 {
		t.Fatal("package must be reproducible and count each role and centre", a, b)
	}
	z, err := zip.OpenReader(first)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	for _, entry := range z.File {
		if entry.Name == ".env" {
			t.Fatal("only map/ and config/ belong to a map package")
		}
	}
}

func TestInvalidMapsRejected(t *testing.T) {
	for _, kind := range []string{"missing-gml", "invalid-gml", "invalid-scenario", "no-agents", "plain-text", "multiple-roots", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := fixture(t)
			switch kind {
			case "missing-gml":
				os.Remove(filepath.Join(root, "map/map.gml"))
			case "invalid-gml":
				os.WriteFile(filepath.Join(root, "map/map.gml"), []byte("<map>"), 0600)
			case "invalid-scenario":
				os.WriteFile(filepath.Join(root, "map/scenario.xml"), []byte("<scenario>"), 0600)
			case "no-agents":
				os.WriteFile(filepath.Join(root, "map/scenario.xml"), []byte("<scenario/>"), 0600)
			case "plain-text":
				os.WriteFile(filepath.Join(root, "map/map.gml"), []byte("not XML"), 0600)
			case "multiple-roots":
				os.WriteFile(filepath.Join(root, "map/map.gml"), []byte("<map/><map/>"), 0600)
			case "symlink":
				os.Symlink("/etc/passwd", filepath.Join(root, "map/link"))
			}
			file := filepath.Join(t.TempDir(), "map.zip")
			err := PackageDirectory(root, file)
			if err == nil {
				_, err = Inspect(file)
			}
			if err == nil {
				t.Fatal("invalid map accepted")
			}
		})
	}
}
