package reproduction

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"simworkflow/internal/experiment"
	"simworkflow/internal/maparchive"
)

const maxTotal = int64(1 << 40)
const maxFile = int64(16 << 30)

type File struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Manifest struct {
	Schema         string            `json:"schema"`
	Workflow       string            `json:"workflow"`
	Phase          string            `json:"phase"`
	CreatedAt      time.Time         `json:"createdAt"`
	Metadata       Metadata          `json:"metadata"`
	Maps           map[string]string `json:"maps"`
	Files          map[string]File   `json:"files"`
	MissingResults []string          `json:"missingResults,omitempty"`
}
type Archive struct {
	Directory string
	Manifest  Manifest
	Plan      experiment.Plan
}

func (a *Archive) Close() { os.RemoveAll(a.Directory) }
func (a *Archive) Image(ref string) (v1.Image, error) {
	if mapped, ok := a.Manifest.Metadata.Images[ref]; ok {
		ref = mapped
	}
	d, err := name.NewDigest(ref)
	if err != nil {
		return nil, errors.New("unpinned bundled image")
	}
	h, err := v1.NewHash(d.DigestStr())
	if err != nil {
		return nil, err
	}
	return layout.Path(filepath.Join(a.Directory, "images")).Image(h)
}
func hashFile(ctx context.Context, path string) (File, error) {
	f, err := os.Open(path)
	if err != nil {
		return File{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, contextReader{ctx, f})
	return File{Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, err
}
func jsonFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}
func safePath(p string) bool {
	return fs.ValidPath(p) && p != "." && filepath.IsLocal(p) && !strings.ContainsAny(p, "\\:")
}

// WriteZIP publishes an archive only after all files and checksums are complete.
func WriteZIP(ctx context.Context, directory, destination string, manifest *Manifest) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	manifest.Files = map[string]File{}
	var paths []string
	var total int64
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("bundle files must be regular files")
		}
		rel, _ := filepath.Rel(directory, path)
		rel = filepath.ToSlash(rel)
		if !safePath(rel) || rel == "manifest.json" {
			return errors.New("invalid bundle path")
		}
		file, err := hashFile(ctx, path)
		if err != nil {
			return err
		}
		if file.Size > maxFile || file.Size > maxTotal-total {
			return errors.New("bundle exceeds size limits")
		}
		total += file.Size
		manifest.Files[rel] = file
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		return err
	}
	if len(paths) > 100000 {
		return errors.New("bundle has too many files")
	}
	if err := jsonFile(filepath.Join(directory, "manifest.json"), manifest); err != nil {
		return err
	}
	paths = append(paths, "manifest.json")
	sort.Strings(paths)
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".simworkflow-export-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	z := zip.NewWriter(tmp)
	for _, path := range paths {
		header := &zip.FileHeader{Name: path, Method: zip.Store}
		header.SetMode(0600)
		if strings.HasSuffix(path, ".json") || strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".md") {
			header.Method = zip.Deflate
		}
		writer, err := z.CreateHeader(header)
		if err != nil {
			z.Close()
			tmp.Close()
			return err
		}
		source, err := os.Open(filepath.Join(directory, filepath.FromSlash(path)))
		if err != nil {
			z.Close()
			tmp.Close()
			return err
		}
		_, err = io.Copy(writer, contextReader{ctx, source})
		source.Close()
		if err != nil {
			z.Close()
			tmp.Close()
			return err
		}
	}
	if err := z.Close(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// A hard link publishes the complete ZIP atomically without overwriting or
	// copying it again. Filesystems without hard links use exclusive copying.
	if err := os.Link(tmp.Name(), destination); err == nil {
		return nil
	} else if os.IsExist(err) {
		return err
	}
	// Exclusive creation keeps existing exports intact on Unix and Windows.
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	source, err := os.Open(tmp.Name())
	if err != nil {
		out.Close()
		os.Remove(destination)
		return err
	}
	_, copyErr := io.Copy(out, contextReader{ctx, source})
	source.Close()
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		os.Remove(destination)
		return errors.New("could not finish export ZIP")
	}
	return nil
}

// Open validates every ZIP member and referenced input before any service writes.
func Open(path string) (*Archive, error) { return OpenContext(context.Background(), path) }

func OpenContext(ctx context.Context, path string) (a *Archive, err error) {
	z, err := zip.OpenReader(path)
	if err != nil {
		return nil, errors.New("invalid reproduction ZIP")
	}
	defer z.Close()
	if len(z.File) > 100001 {
		return nil, errors.New("too many ZIP members")
	}
	members := map[string]*zip.File{}
	var total int64
	for _, f := range z.File {
		if !safePath(f.Name) || !f.Mode().IsRegular() || members[f.Name] != nil || f.UncompressedSize64 > uint64(maxFile) || f.UncompressedSize64 > uint64(maxTotal-total) {
			return nil, errors.New("unsafe, duplicate or oversized ZIP member")
		}
		total += int64(f.UncompressedSize64)
		members[f.Name] = f
	}
	mf := members["manifest.json"]
	if mf == nil || mf.UncompressedSize64 > 32<<20 {
		return nil, errors.New("missing or oversized bundle manifest")
	}
	r, err := mf.Open()
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&manifest)
	r.Close()
	if err != nil || manifest.Schema != Schema || manifest.Metadata.Schema != Schema || manifest.Metadata.Platform != "linux/amd64" || len(manifest.Files) != len(members)-1 {
		return nil, errors.New("invalid bundle manifest")
	}
	if mode := manifest.Metadata.ConfigStorage; mode != "" && mode != "s3" && mode != "s3-bundle" && mode != "s3-sequence" && mode != "inline" {
		return nil, errors.New("unsupported worker configuration format")
	}
	dir, err := os.MkdirTemp("", "simworkflow-reproduce-*")
	if err != nil {
		return nil, err
	}
	a = &Archive{Directory: dir, Manifest: manifest}
	defer func() {
		if err != nil {
			os.RemoveAll(dir)
		}
	}()
	for name, expected := range manifest.Files {
		entry := members[name]
		if entry == nil || name == "manifest.json" || !safePath(name) || expected.Size != int64(entry.UncompressedSize64) || !experiment.ValidSHA256(expected.SHA256) {
			return nil, errors.New("bundle file inventory mismatch")
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return nil, err
		}
		input, e := entry.Open()
		if e != nil {
			return nil, e
		}
		out, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			input.Close()
			return nil, e
		}
		h := sha256.New()
		_, e = io.Copy(io.MultiWriter(out, h), contextReader{ctx, input})
		input.Close()
		closeErr := out.Close()
		if e != nil || closeErr != nil || hex.EncodeToString(h.Sum(nil)) != expected.SHA256 {
			return nil, errors.New("bundle file checksum mismatch")
		}
		if strings.HasPrefix(name, "images/blobs/sha256/") && strings.TrimPrefix(name, "images/blobs/sha256/") != expected.SHA256 {
			return nil, errors.New("OCI blob digest mismatch")
		}
	}
	planFile, ok := manifest.Files["plan.json"]
	if !ok || planFile.Size > 32<<20 {
		return nil, errors.New("missing or oversized plan")
	}
	data, e := os.ReadFile(filepath.Join(dir, "plan.json"))
	if e != nil {
		return nil, e
	}
	if err = json.Unmarshal(data, &a.Plan); err != nil {
		return nil, errors.New("invalid bundled plan")
	}
	if err = ValidatePlan(&a.Plan); err != nil {
		return nil, err
	}
	if saved := manifest.Metadata.PlanSHA256; saved != "" && saved != PlanSHA256(&a.Plan) {
		return nil, errors.New("bundled plan differs from reproduction metadata")
	}
	requiredImages := map[string]bool{manifest.Metadata.HelperImage: true}
	requiredMaps := map[string]bool{}
	for _, run := range a.Plan.Runs {
		requiredImages[run.Simulator.Image] = true
		requiredImages[run.Configuration.Image] = true
		requiredMaps[run.Map.ExpectedSHA256()] = true
	}
	if len(manifest.Maps) != len(requiredMaps) {
		return nil, errors.New("unexpected map in bundle inventory")
	}
	for ref := range requiredImages {
		image, e := a.Image(ref)
		if e != nil {
			return nil, errors.New("required image is absent from ZIP")
		}
		if err = ValidateImage(ctx, image); err != nil {
			return nil, err
		}
	}
	for sha := range requiredMaps {
		file, ok := manifest.Maps[sha]
		record, present := manifest.Files[file]
		if !ok || !present || !experiment.ValidSHA256(sha) || file != "maps/"+sha+".zip" || record.SHA256 != sha {
			return nil, errors.New("missing or mismatched bundled map")
		}
		info, e := maparchive.Inspect(filepath.Join(dir, filepath.FromSlash(file)))
		if e != nil || info.SHA256 != sha {
			return nil, errors.New("invalid bundled map")
		}
	}
	return a, nil
}
func ValidateImage(ctx context.Context, image v1.Image) error {
	manifest, err := image.Manifest()
	if err != nil {
		return errors.New("invalid image manifest")
	}
	for _, descriptor := range append([]v1.Descriptor{manifest.Config}, manifest.Layers...) {
		if descriptor.Digest.Algorithm != "sha256" || !experiment.ValidSHA256(descriptor.Digest.Hex) || descriptor.Size < 0 || descriptor.Size > maxFile {
			return errors.New("invalid OCI blob descriptor")
		}
	}
	cfg, err := image.ConfigFile()
	if err != nil || cfg.OS != "linux" || cfg.Architecture != "amd64" {
		return errors.New("bundled image must be linux/amd64")
	}
	raw, err := image.RawManifest()
	if err != nil {
		return errors.New("invalid image manifest")
	}
	digest, err := image.Digest()
	if err != nil || digest.Algorithm != "sha256" || fmtHash(raw) != digest.Hex {
		return errors.New("image manifest digest mismatch")
	}
	raw, err = image.RawConfigFile()
	if err != nil {
		return errors.New("missing image configuration")
	}
	configDigest, err := image.ConfigName()
	if err != nil || configDigest.Algorithm != "sha256" || fmtHash(raw) != configDigest.Hex || int64(len(raw)) != manifest.Config.Size {
		return errors.New("image configuration digest mismatch")
	}
	layers, err := image.Layers()
	if err != nil || len(layers) != len(manifest.Layers) {
		return errors.New("missing image layers")
	}
	for i, layer := range layers {
		digest, err := layer.Digest()
		if err != nil {
			return errors.New("invalid image layer")
		}
		body, err := layer.Compressed()
		if err != nil {
			return errors.New("missing image layer blob")
		}
		h := sha256.New()
		n, copyErr := io.Copy(h, io.LimitReader(contextReader{ctx, body}, maxFile+1))
		body.Close()
		if copyErr != nil || n > maxFile || n != manifest.Layers[i].Size || digest.Algorithm != "sha256" || hex.EncodeToString(h.Sum(nil)) != digest.Hex {
			return errors.New("image layer digest mismatch")
		}
	}
	return nil
}
func fmtHash(data []byte) string { value := sha256.Sum256(data); return hex.EncodeToString(value[:]) }
