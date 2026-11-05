package resultarchive

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestSmallLogDoesNotReadHugeNeighbour(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "bundle.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z := zip.NewWriter(f)
	large, err := z.CreateHeader(&zip.FileHeader{Name: "logs/simulation/kernel.log", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(large, zeros{}, 32<<20); err != nil {
		t.Fatal(err)
	}
	score, err := z.Create("logs/simulation/score.log")
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(score, "score: 123\n")
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	info, _ := f.Stat()
	read := 0
	r, err := NewReader(f, info.Size(), func(n int) { read += n })
	if err != nil {
		t.Fatal(err)
	}
	body, err := r.OpenFile(r.File[1])
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil || string(data) != "score: 123\n" || read >= 1<<20 {
		t.Fatalf("err=%v data=%q bytes-read=%d", err, data, read)
	}
}

func TestZIP64InventoryAndForgedCounts(t *testing.T) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for i := range 65535 {
		if _, err := z.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("files/%05d", i), Method: zip.Store}); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	data := b.Bytes()
	r, err := NewReader(bytes.NewReader(data), int64(len(data)), nil)
	if err != nil || len(r.File) != 65535 {
		t.Fatal("valid ZIP64 inventory must remain supported", err)
	}
	locator := data[len(data)-22-20:]
	offset := binary.LittleEndian.Uint64(locator[8:])
	binary.LittleEndian.PutUint64(data[offset+24:], 1<<30)
	binary.LittleEndian.PutUint64(data[offset+32:], 1<<30)
	if _, err := NewReader(bytes.NewReader(data), int64(len(data)), nil); err == nil || !strings.Contains(err.Error(), "entry or size limit") {
		t.Fatal("forged ZIP64 counts must be rejected before standard-library preallocation", err)
	}
}

func TestZIPRejectsUnsafeInventoryAndChecksSelectedCRC(t *testing.T) {
	for _, name := range []string{"../outside", "logs/../../outside", "logs\\outside"} {
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		w, _ := z.Create(name)
		io.WriteString(w, "data")
		z.Close()
		if _, err := NewReader(bytes.NewReader(b.Bytes()), int64(b.Len()), nil); err == nil {
			t.Fatal("accepted unsafe entry", name)
		}
	}
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, _ := z.CreateHeader(&zip.FileHeader{Name: "logs/simulation/score.log", Method: zip.Store})
	io.WriteString(w, "score: 123")
	z.Close()
	data := b.Bytes()
	original, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	offset, _ := original.File[0].DataOffset()
	data[offset] ^= 1
	r, err := NewReader(bytes.NewReader(data), int64(len(data)), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := r.OpenFile(r.File[0])
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	if _, err := io.ReadAll(body); err != zip.ErrChecksum {
		t.Fatal("must verify the requested file's CRC", err)
	}
}
