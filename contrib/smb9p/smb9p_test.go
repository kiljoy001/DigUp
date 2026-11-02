package main

import (
	"io"
	"strings"
	"testing"
	"time"
)

func fileNode(name string, body string) *fsNode {
	n := &fsNode{id: newID(), name: name, mode: 0444, mtime: uint32(time.Now().Unix())}
	n.size = func() uint64 { return uint64(len(body)) }
	n.open = func() (io.ReaderAt, io.Closer, error) {
		return strings.NewReader(body), io.NopCloser(nil), nil
	}
	return n
}

// SMB is case-insensitive; a client that lists "Lease.md" may walk "lease.md".
func TestWalkCaseInsensitive(t *testing.T) {
	child := fileNode("Lease.md", "x")
	d := &fsNode{id: newID(), name: "/", dir: true, mode: DMDIR | 0555,
		list: func() []*fsNode { return []*fsNode{child} }}
	got, err := d.walk("lease.md")
	if err != nil || got != child {
		t.Fatalf("walk: %v, %v", got, err)
	}
	if _, err := d.walk("missing"); err == nil {
		t.Fatal("walk of a missing name should fail")
	}
}

func TestDirSliceKeepsEntriesWhole(t *testing.T) {
	var kids []*fsNode
	for i := 0; i < 20; i++ {
		kids = append(kids, fileNode(strings.Repeat("n", i+1)+".txt", "x"))
	}
	d := &fsNode{id: newID(), name: "d", dir: true, mode: DMDIR | 0555,
		list: func() []*fsNode { return kids }}
	var all []byte
	var off uint64
	for {
		data := d.dirSlice(off, 128)
		if len(data) == 0 {
			break
		}
		all = append(all, data...)
		off += uint64(len(data))
	}
	count := 0
	for len(all) > 0 {
		_, n, err := parseDirForTest(all)
		if err != nil {
			t.Fatalf("entry %d: %v", count, err)
		}
		all = all[n:]
		count++
	}
	if count != len(kids) {
		t.Fatalf("parsed %d entries, want %d", count, len(kids))
	}
}

// parseDirForTest reads one stat structure's size prefix, which is all this
// test needs to check that entries are not split.
func parseDirForTest(b []byte) (int, int, error) {
	if len(b) < 2 {
		return 0, 0, io.ErrUnexpectedEOF
	}
	size := int(b[0]) | int(b[1])<<8
	if 2+size > len(b) {
		return 0, 0, io.ErrUnexpectedEOF
	}
	return 0, 2 + size, nil
}

func TestStatModes(t *testing.T) {
	f := fileNode("a.txt", "hello")
	if _, mode, length, _ := f.stat(); mode != 0444 || length != 5 {
		t.Fatalf("file mode %o length %d", mode, length)
	}
	d := &fsNode{id: newID(), name: "d", dir: true, mode: DMDIR | 0555}
	if _, mode, _, _ := d.stat(); mode != DMDIR|0555 {
		t.Fatalf("dir mode %o", mode)
	}
}
