package worker

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type countedLog struct {
	io.ReaderAt
	bytes int
}

func (r *countedLog) ReadAt(p []byte, off int64) (int, error) {
	n, err := r.ReaderAt.ReadAt(p, off)
	r.bytes += n
	return n, err
}

func TestLastScoreReverseLinesAndBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       *float64
	}{
		{"empty", "", nil},
		{"missing", "NotAScore: 9\n", nil},
		{"last without newline", "Score: 3\nSCORE = -2.5e-3", scoreValue(-0.0025)},
		{"multiple on line", "Score: 1 Score: 2\n", scoreValue(2)},
		{"chunk boundary", "Score: 8.25\n" + strings.Repeat("x", (64<<10)-5), scoreValue(8.25)},
		{"no false word boundary", "Score: 4\nNotAScore: 9" + strings.Repeat("x", 64<<10), scoreValue(4)},
		{"oversized unrelated line", "Score: 7\n" + strings.Repeat("x", 5<<20) + "\n", scoreValue(7)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := lastScore(strings.NewReader(tc.text), int64(len(tc.text)))
			if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
func scoreValue(v float64) *float64 { return &v }

func TestLastScoreDoesNotScanGigabyteLog(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "kernel.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	const size = int64(1 << 30)
	if err = f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	suffix := []byte("\nINFO kernel : Score: 12.5\nKernel has shut down\n")
	if _, err = f.WriteAt(suffix, size-int64(len(suffix))); err != nil {
		t.Fatal(err)
	}
	r := &countedLog{ReaderAt: f}
	got := lastScore(r, size)
	if got == nil || *got != 12.5 {
		t.Fatal("missing final score", got)
	}
	if r.bytes > 64<<10 {
		t.Fatalf("read %d bytes from a gigabyte log", r.bytes)
	}
}
