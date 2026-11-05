// Package resultarchive reads result ZIPs using bounded, cached random access.
package resultarchive

import (
	"archive/zip"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
)

const MaxEntries = 100000
const MaxExpanded = uint64(16 << 30)
const MaxIndex = int64(32 << 20)

type Reader struct {
	*zip.Reader
	cache *cacheReader
}

// NewReader validates the entire inventory before any files are extracted.
// onRead counts actual underlying bytes, including ZIP metadata and read-ahead.
func NewReader(r io.ReaderAt, size int64, onRead func(int)) (*Reader, error) {
	if size < 0 || uint64(size) > MaxExpanded+uint64(MaxIndex) {
		return nil, errors.New("result ZIP exceeds size limit")
	}
	c := &cacheReader{source: r, size: size, indexing: true, onRead: onRead}
	if err := validateDirectory(c, size); err != nil {
		return nil, err
	}
	z, err := zip.NewReader(c, size)
	if err != nil {
		return nil, err
	}
	c.indexing = false
	if len(z.File) > MaxEntries {
		return nil, errors.New("result ZIP exceeds entry limit")
	}
	seen := map[string]bool{}
	var expanded uint64
	for _, file := range z.File {
		name := strings.TrimSuffix(file.Name, "/")
		mode := file.Mode()
		if !fs.ValidPath(name) || !filepath.IsLocal(name) || name == "." || strings.Contains(name, "\\") || seen[name] || (!mode.IsRegular() && !mode.IsDir()) || file.Flags&1 != 0 {
			return nil, errors.New("result ZIP contains unsafe, duplicate or encrypted entries")
		}
		seen[name] = true
		if file.UncompressedSize64 > MaxExpanded-expanded || file.CompressedSize64 > uint64(size) {
			return nil, errors.New("result ZIP exceeds expanded size limit")
		}
		expanded += file.UncompressedSize64
	}
	return &Reader{Reader: z, cache: c}, nil
}

// Check counts before archive/zip preallocates its file inventory. These result
// archives have no SFX prefix or trailing data; both normal ZIP and ZIP64 are
// supported. Walking directory headers also detects forged record counts.
func validateDirectory(r io.ReaderAt, size int64) error {
	if size < 22 {
		return zip.ErrFormat
	}
	tail := make([]byte, min(size, 22+65535))
	if _, err := r.ReadAt(tail, size-int64(len(tail))); err != nil {
		return err
	}
	position := -1
	for i := len(tail) - 22; i >= 0; i-- {
		if binary.LittleEndian.Uint32(tail[i:]) == 0x06054b50 && i+22+int(binary.LittleEndian.Uint16(tail[i+20:])) == len(tail) {
			position = i
			break
		}
	}
	if position < 0 {
		return zip.ErrFormat
	}
	end := tail[position:]
	if binary.LittleEndian.Uint32(end[4:]) != 0 || binary.LittleEndian.Uint16(end[8:]) != binary.LittleEndian.Uint16(end[10:]) {
		return zip.ErrFormat
	}
	count := uint64(binary.LittleEndian.Uint16(end[10:]))
	directorySize := uint64(binary.LittleEndian.Uint32(end[12:]))
	offset := uint64(binary.LittleEndian.Uint32(end[16:]))
	endOffset := uint64(size-int64(len(tail))) + uint64(position)
	if count == 0xffff || directorySize == 0xffffffff || offset == 0xffffffff {
		if endOffset < 20 {
			return zip.ErrFormat
		}
		var locator [20]byte
		if _, err := r.ReadAt(locator[:], int64(endOffset)-20); err != nil {
			return err
		}
		if binary.LittleEndian.Uint32(locator[:]) != 0x07064b50 || binary.LittleEndian.Uint32(locator[4:]) != 0 || binary.LittleEndian.Uint32(locator[16:]) != 1 {
			return zip.ErrFormat
		}
		endOffset = binary.LittleEndian.Uint64(locator[8:])
		if endOffset > uint64(size)-56 {
			return zip.ErrFormat
		}
		var record [56]byte
		if _, err := r.ReadAt(record[:], int64(endOffset)); err != nil {
			return err
		}
		if binary.LittleEndian.Uint32(record[:]) != 0x06064b50 || binary.LittleEndian.Uint64(record[4:]) < 44 || binary.LittleEndian.Uint64(record[16:]) != 0 || binary.LittleEndian.Uint64(record[24:]) != binary.LittleEndian.Uint64(record[32:]) {
			return zip.ErrFormat
		}
		count = binary.LittleEndian.Uint64(record[32:])
		directorySize = binary.LittleEndian.Uint64(record[40:])
		offset = binary.LittleEndian.Uint64(record[48:])
	}
	if count > MaxEntries || directorySize > uint64(MaxIndex) {
		return errors.New("result ZIP directory exceeds entry or size limit")
	}
	if offset > endOffset || directorySize != endOffset-offset {
		return zip.ErrFormat
	}
	var seen uint64
	for next := offset; next < endOffset; {
		var header [46]byte
		if endOffset-next < 46 || seen >= MaxEntries {
			return zip.ErrFormat
		}
		if _, err := r.ReadAt(header[:], int64(next)); err != nil {
			return err
		}
		if binary.LittleEndian.Uint32(header[:]) != 0x02014b50 {
			return zip.ErrFormat
		}
		length := uint64(46) + uint64(binary.LittleEndian.Uint16(header[28:])) + uint64(binary.LittleEndian.Uint16(header[30:])) + uint64(binary.LittleEndian.Uint16(header[32:]))
		if length > endOffset-next {
			return zip.ErrFormat
		}
		next += length
		seen++
	}
	if seen != count {
		return zip.ErrFormat
	}
	return nil
}

// OpenFile keeps data read-ahead inside this entry's compressed byte range.
// The standard ZIP reader verifies the decompressed size and CRC at EOF.
func (r *Reader) OpenFile(file *zip.File) (io.ReadCloser, error) {
	offset, err := file.DataOffset()
	if err != nil {
		return nil, err
	}
	if offset < 0 || offset > r.cache.size || file.CompressedSize64 > uint64(r.cache.size-offset) {
		return nil, errors.New("result ZIP entry lies outside the archive")
	}
	r.cache.mu.Lock()
	r.cache.start, r.cache.end = offset, offset+int64(file.CompressedSize64)
	r.cache.mu.Unlock()
	return file.Open()
}

type block struct {
	offset int64
	data   []byte
}

type cacheReader struct {
	mu               sync.Mutex
	source           io.ReaderAt
	size, start, end int64
	indexing         bool
	indexBytes       int64
	blocks           []block
	onRead           func(int)
}

func (r *cacheReader) ReadAt(p []byte, offset int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if offset < 0 {
		return 0, errors.New("negative result ZIP offset")
	}
	n := 0
	for len(p) > 0 {
		if offset >= r.size {
			return n, io.EOF
		}
		var data []byte
		for _, cached := range r.blocks {
			if offset >= cached.offset && offset < cached.offset+int64(len(cached.data)) {
				data = cached.data[offset-cached.offset:]
				break
			}
		}
		if data == nil {
			// Metadata uses 64KiB blocks; selected data uses at most 1MiB,
			// clipped to the entry so tiny logs never fetch huge neighbours.
			start := offset / (64 << 10) * (64 << 10)
			end := min(r.size, start+(64<<10))
			if offset >= r.start && offset < r.end {
				start = r.start + (offset-r.start)/(1<<20)*(1<<20)
				end = min(r.end, start+(1<<20))
			}
			if r.indexing && end-start > 2*MaxIndex+(256<<10)-r.indexBytes {
				return n, errors.New("result ZIP index exceeds size limit")
			}
			buffer := make([]byte, end-start)
			read, err := r.source.ReadAt(buffer, start)
			if r.onRead != nil && read > 0 {
				r.onRead(read)
			}
			r.indexBytes += int64(read)
			if read != len(buffer) {
				if err == nil {
					err = io.ErrUnexpectedEOF
				}
				return n, err
			}
			if err != nil && err != io.EOF {
				return n, err
			}
			if len(r.blocks) == 4 {
				r.blocks = r.blocks[1:]
			}
			r.blocks = append(r.blocks, block{start, buffer})
			data = buffer[offset-start:]
		}
		copied := copy(p, data)
		n += copied
		offset += int64(copied)
		p = p[copied:]
	}
	return n, nil
}
