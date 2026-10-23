package maparchive

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Counts struct {
	FireBrigade     int `json:"fireBrigade"`
	FireStation     int `json:"fireStation"`
	PoliceForce     int `json:"policeForce"`
	PoliceOffice    int `json:"policeOffice"`
	AmbulanceTeam   int `json:"ambulanceTeam"`
	AmbulanceCentre int `json:"ambulanceCentre"`
}

type Info struct {
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes"`
	Agents    Counts `json:"agents"`
}

func Inspect(path string) (Info, error) {
	var info Info
	f, err := os.Open(path)
	if err != nil {
		return info, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return info, err
	}
	if !stat.Mode().IsRegular() || stat.Size() > MaxBytes {
		return info, errors.New("map ZIP must be a regular file of at most 2GiB")
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return info, err
	}
	info.SHA256 = hex.EncodeToString(hash.Sum(nil))
	info.SizeBytes = stat.Size()
	z, err := zip.OpenReader(path)
	if err != nil {
		return info, errors.New("map must be a ZIP archive")
	}
	for _, entry := range z.File {
		name := strings.TrimSuffix(entry.Name, "/")
		if name == "" || name == "." {
			continue
		}
		first := strings.SplitN(name, "/", 2)[0]
		if first != "map" && first != "config" {
			z.Close()
			return info, errors.New("map ZIP must contain only map/ and config/")
		}
	}
	z.Close()
	root, err := os.MkdirTemp("", "simworkflow-map-check-*")
	if err != nil {
		return info, err
	}
	defer os.RemoveAll(root)
	if err = ExtractZIP(path, root); err != nil {
		return info, err
	}
	for _, name := range []string{"map/map.gml", "map/scenario.xml", "config/kernel.cfg", "config/common.cfg"} {
		stat, err := os.Stat(filepath.Join(root, name))
		if err != nil || !stat.Mode().IsRegular() || stat.Size() == 0 {
			return info, fmt.Errorf("map archive must contain nonempty %s", name)
		}
	}
	for _, name := range []string{"map/map.gml", "map/scenario.xml"} {
		if err := validateXML(filepath.Join(root, name)); err != nil {
			return info, fmt.Errorf("%s: %w", name, err)
		}
	}
	counts, err := ScenarioCounts(filepath.Join(root, "map/scenario.xml"))
	if err != nil {
		return info, err
	}
	info.Agents = Counts{counts[0], counts[1], counts[2], counts[3], counts[4], counts[5]}
	return info, nil
}

// PackageDirectory includes only the map/ and config/ trees. ZIP timestamps and
// file modes are fixed so identical directory content produces identical bytes.
func PackageDirectory(root, destination string) (err error) {
	f, err := os.Create(destination)
	if err != nil {
		return err
	}
	z := zip.NewWriter(f)
	defer func() {
		for _, close := range []func() error{z.Close, f.Close} {
			if e := close(); err == nil && e != nil {
				err = e
			}
		}
	}()
	var size int64
	count := 0
	for _, tree := range []string{"map", "config"} {
		err = filepath.WalkDir(filepath.Join(root, tree), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("map directory must not contain symlinks")
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("map directory must contain regular files")
			}
			count++
			if count > 10000 || info.Size() > MaxBytes-size {
				return errors.New("map directory exceeds archive limits")
			}
			size += info.Size()
			name, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			header := &zip.FileHeader{Name: filepath.ToSlash(name), Method: zip.Deflate}
			header.SetMode(0644)
			header.SetModTime(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
			target, err := z.CreateHeader(header)
			if err != nil {
				return err
			}
			source, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(target, source)
			closeErr := source.Close()
			if copyErr != nil {
				return copyErr
			}
			return closeErr
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// validateXML does not resolve external entities or fetch external resources.
func validateXML(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	decoder := xml.NewDecoder(f)
	depth, roots := 0, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			if depth == 0 && roots == 1 {
				return nil
			}
			return errors.New("XML must contain one root element")
		}
		if err != nil {
			return errors.New("invalid XML")
		}
		switch value := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(value)) != "" {
				return errors.New("text outside XML root element")
			}
		}
	}
}
