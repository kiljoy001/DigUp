// The SMB half: one session, one share, read-only, exposed as nodes.
package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hirochachacha/go-smb2"
)

type backend struct {
	share *smb2.Share
}

func dialSMB(server, share, user, pass, domain string) (*backend, error) {
	conn, err := net.DialTimeout("tcp", server, 15*time.Second)
	if err != nil {
		return nil, err
	}
	d := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{
		User: user, Password: pass, Domain: domain,
	}}
	s, err := d.Dial(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	fs, err := s.Mount(share)
	if err != nil {
		s.Logoff()
		conn.Close()
		return nil, err
	}
	logf("mounted %s on %s as %s", share, server, user)
	return &backend{share: fs}, nil
}

func (b *backend) root(sub string) (*fsNode, error) {
	name := "/"
	mtime := uint32(time.Now().Unix())
	if sub != "" {
		fi, err := b.share.Stat(strings.Trim(sub, "/"))
		if err != nil {
			return nil, err
		}
		name = fi.Name()
		mtime = uint32(fi.ModTime().Unix())
	}
	n := &fsNode{id: newID(), name: name, dir: true, mode: DMDIR | 0555, mtime: mtime}
	sub = strings.Trim(sub, "/")
	n.list = func() []*fsNode { return b.children(n, sub) }
	return n, nil
}

func (b *backend) children(parent *fsNode, p string) []*fsNode {
	infos, err := b.share.ReadDir(p)
	if err != nil {
		logf("readdir %q: %v", p, err)
		return nil
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name() < infos[j].Name() })
	out := make([]*fsNode, 0, len(infos))
	for _, fi := range infos {
		name := fi.Name()
		if name == "" || name == "." || name == ".." {
			continue
		}
		out = append(out, b.node(parent, name, joinPath(p, name), fi))
	}
	return out
}

func joinPath(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

func (b *backend) node(parent *fsNode, name, path string, fi os.FileInfo) *fsNode {
	n := &fsNode{
		id:     newID(),
		name:   name,
		dir:    fi.IsDir(),
		mtime:  uint32(fi.ModTime().Unix()),
		parent: parent,
	}
	if n.dir {
		n.mode = DMDIR | 0555
		n.list = func() []*fsNode { return b.children(n, path) }
	} else {
		n.mode = 0444
		size := fi.Size()
		n.size = func() uint64 { return uint64(size) }
		n.open = func() (io.ReaderAt, io.Closer, error) {
			f, err := b.share.Open(path)
			if err != nil {
				return nil, nil, err
			}
			return f, f, nil
		}
	}
	return n
}

func (b *backend) close() {
	if b.share != nil {
		b.share.Umount()
	}
}

func describe(fi os.FileInfo) string {
	kind := "file"
	if fi.IsDir() {
		kind = "dir"
	}
	return fmt.Sprintf("%s %s %d", fi.Name(), kind, fi.Size())
}
