// The node type the 9P server walks: enough of a filesystem for a reader.
package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

type fsNode struct {
	id     uint64
	name   string
	dir    bool
	mode   uint32
	mtime  uint32
	parent *fsNode
	size   func() uint64
	open   func() (io.ReaderAt, io.Closer, error)
	list   func() []*fsNode
}

var nextNodeID uint64

func newID() uint64 { return atomic.AddUint64(&nextNodeID, 1) }

func (n *fsNode) walk(name string) (*fsNode, error) {
	switch name {
	case "", ".":
		return n, nil
	case "..":
		if n.parent != nil {
			return n.parent, nil
		}
		return n, nil
	}
	if !n.dir || n.list == nil {
		return nil, errNotDir
	}
	kids := n.list()
	for _, c := range kids {
		if c.name == name {
			return c, nil
		}
	}
	// SMB names are case-insensitive; a 9P client may not preserve the case
	// it saw in a listing.
	for _, c := range kids {
		if strings.EqualFold(c.name, name) {
			return c, nil
		}
	}
	return nil, errNotExist
}

func (n *fsNode) stat() (name string, mode uint32, length uint64, mtime uint32) {
	length = 0
	if !n.dir && n.size != nil {
		length = n.size()
	}
	return n.name, n.mode, length, n.mtime
}

// dirSlice returns whole directory entries from byte offset off.
func (n *fsNode) dirSlice(off uint64, count uint32) []byte {
	kids := n.list()
	sort.Slice(kids, func(i, j int) bool { return kids[i].name < kids[j].name })
	var out []byte
	var pos uint64
	for _, c := range kids {
		e := encodeStat(c)
		if pos+uint64(len(e)) <= off {
			pos += uint64(len(e))
			continue
		}
		if len(out)+len(e) > int(count) {
			break
		}
		out = append(out, e...)
		pos += uint64(len(e))
	}
	return out
}

func encodeStat(n *fsNode) []byte {
	w := &wbuf{}
	w.u16(0)
	w.u16(0)
	w.u32(0)
	qid(w, n)
	_, mode, length, mtime := n.stat()
	w.u32(mode)
	w.u32(mtime)
	w.u32(mtime)
	w.u64(length)
	w.s(n.name)
	w.s("smb9p")
	w.s("smb9p")
	w.s("smb9p")
	binary.LittleEndian.PutUint16(w.b[0:2], uint16(len(w.b)-2))
	return w.b
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" || p == "." {
		return nil
	}
	return strings.Split(p, "/")
}

func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "smb9p %s ", time.Now().Format("15:04:05"))
	fmt.Fprintf(os.Stderr, format, a...)
	fmt.Fprintln(os.Stderr)
}
