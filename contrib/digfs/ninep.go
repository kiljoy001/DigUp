// 9P2000, server side. Enough of the protocol for a read-mostly filesystem
// with one write-only control file: version, attach, walk, open, read, write,
// clunk, stat. Auth, create, remove and wstat are refused, as they are on most
// Plan 9 service filesystems.
package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
)

const (
	Tversion = 100
	Rversion = 101
	Tauth    = 102
	Rauth    = 103
	Tattach  = 104
	Rattach  = 105
	Rerror   = 107
	Tflush   = 108
	Rflush   = 109
	Twalk    = 110
	Rwalk    = 111
	Topen    = 112
	Ropen    = 113
	Tcreate  = 114
	Rcreate  = 115
	Tread    = 116
	Rread    = 117
	Twrite   = 118
	Rwrite   = 119
	Tclunk   = 120
	Rclunk   = 121
	Tremove  = 122
	Rremove  = 123
	Tstat    = 124
	Rstat    = 125
	Twstat   = 126
	Rwstat   = 127
)

const (
	QTDIR  = 0x80
	QTFILE = 0x00

	DMDIR   = 0x80000000
	DMREAD  = 0x4
	DMWRITE = 0x2
	DMEXEC  = 0x1

	OREAD  = 0
	OWRITE = 1
	ORDWR  = 2
	OEXEC  = 3
	OTRUNC = 0x10

	NOFID = 0xffffffff
)

var (
	errNotExist  = errors.New("does not exist")
	errNotDir    = errors.New("not a directory")
	errIsDir     = errors.New("is a directory")
	errPerm      = errors.New("permission denied")
	errTooMany   = errors.New("too many path elements")
	errNotOpen   = errors.New("file not open")
	errBadOffset = errors.New("bad offset")
	errBadMsg    = errors.New("bad message")
)

// wbuf builds a reply body.
type wbuf struct{ b []byte }

func (w *wbuf) u8(v uint8)   { w.b = append(w.b, v) }
func (w *wbuf) u16(v uint16) { w.b = binary.LittleEndian.AppendUint16(w.b, v) }
func (w *wbuf) u32(v uint32) { w.b = binary.LittleEndian.AppendUint32(w.b, v) }
func (w *wbuf) u64(v uint64) { w.b = binary.LittleEndian.AppendUint64(w.b, v) }
func (w *wbuf) s(v string) {
	if len(v) > 0xffff {
		v = v[:0xffff]
	}
	w.u16(uint16(len(v)))
	w.b = append(w.b, v...)
}

// rbuf reads a request body.
type rbuf struct {
	b   []byte
	bad bool
}

func (r *rbuf) u8() uint8 {
	if r.bad || len(r.b) < 1 {
		r.bad = true
		return 0
	}
	v := r.b[0]
	r.b = r.b[1:]
	return v
}

func (r *rbuf) u16() uint16 {
	if r.bad || len(r.b) < 2 {
		r.bad = true
		return 0
	}
	v := binary.LittleEndian.Uint16(r.b)
	r.b = r.b[2:]
	return v
}

func (r *rbuf) u32() uint32 {
	if r.bad || len(r.b) < 4 {
		r.bad = true
		return 0
	}
	v := binary.LittleEndian.Uint32(r.b)
	r.b = r.b[4:]
	return v
}

func (r *rbuf) u64() uint64 {
	if r.bad || len(r.b) < 8 {
		r.bad = true
		return 0
	}
	v := binary.LittleEndian.Uint64(r.b)
	r.b = r.b[8:]
	return v
}

func (r *rbuf) s() string {
	n := int(r.u16())
	if r.bad || len(r.b) < n {
		r.bad = true
		return ""
	}
	v := string(r.b[:n])
	r.b = r.b[n:]
	return v
}

func (r *rbuf) count() []byte {
	n := int(r.u32())
	if r.bad || len(r.b) < n {
		r.bad = true
		return nil
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}

type fid struct {
	n    *fsNode
	open bool
	mode uint8
	wbuf []byte
}

type server struct {
	tree *tree
}

// plan9addr turns "tcp!host!port" (host may be "*") into something net.Listen
// understands. A plain host:port is accepted too.
func plan9addr(a string) (network, addr string, err error) {
	if !strings.Contains(a, "!") {
		return "tcp", a, nil
	}
	p := strings.SplitN(a, "!", 3)
	if len(p) != 3 {
		return "", "", fmt.Errorf("address %q: want net!host!port", a)
	}
	host := p[1]
	if host == "*" {
		host = ""
	}
	return p[0], net.JoinHostPort(host, p[2]), nil
}

func (s *server) listen(a string) error {
	network, addr, err := plan9addr(a)
	if err != nil {
		return err
	}
	l, err := net.Listen(network, addr)
	if err != nil {
		return err
	}
	logf("9p: listening on %s", l.Addr())
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		go s.serve(c)
	}
}

func (s *server) serve(c net.Conn) {
	defer c.Close()
	fids := make(map[uint32]*fid)
	var msize uint32 = 8192

	hdr := make([]byte, 4)
	for {
		if _, err := io.ReadFull(c, hdr); err != nil {
			return
		}
		size := binary.LittleEndian.Uint32(hdr)
		if size < 7 || size > 4<<20 {
			return
		}
		msg := make([]byte, size)
		copy(msg, hdr)
		if _, err := io.ReadFull(c, msg[4:]); err != nil {
			return
		}
		typ := msg[4]
		tag := binary.LittleEndian.Uint16(msg[5:7])
		r := &rbuf{b: msg[7:]}

		rtyp, body, rerr := s.dispatch(typ, r, fids, &msize)
		if r.bad {
			rtyp, body, rerr = Rerror, nil, errBadMsg
		}
		if rerr != nil {
			rtyp = Rerror
			w := &wbuf{}
			w.s(rerr.Error())
			body = w.b
		}
		out := &wbuf{}
		out.u32(0)
		out.u8(rtyp)
		out.u16(tag)
		out.b = append(out.b, body...)
		binary.LittleEndian.PutUint32(out.b, uint32(len(out.b)))
		if _, err := c.Write(out.b); err != nil {
			return
		}
	}
}

func qid(w *wbuf, n *fsNode) {
	typ := uint8(QTFILE)
	if n.dir {
		typ = QTDIR
	}
	w.u8(typ)
	w.u32(0)
	w.u64(n.id)
}

func (s *server) dispatch(typ uint8, r *rbuf, fids map[uint32]*fid, msize *uint32) (uint8, []byte, error) {
	switch typ {
	case Tversion:
		req := r.u32()
		ver := r.s()
		if !strings.HasPrefix(ver, "9P2000") {
			w := &wbuf{}
			w.u32(*msize)
			w.s("unknown")
			return Rversion, w.b, nil
		}
		if req < *msize {
			*msize = req
		}
		if *msize < 512 {
			*msize = 512
		}
		w := &wbuf{}
		w.u32(*msize)
		w.s("9P2000")
		return Rversion, w.b, nil

	case Tauth:
		return Rerror, nil, errors.New("authentication not required")

	case Tattach:
		f, afid, uname, aname := r.u32(), r.u32(), r.s(), r.s()
		_ = afid
		_ = uname
		if r.bad {
			return Rerror, nil, errBadMsg
		}
		if aname != "" && aname != "/" {
			return Rerror, nil, errNotExist
		}
		fids[f] = &fid{n: s.tree.root}
		w := &wbuf{}
		qid(w, s.tree.root)
		return Rattach, w.b, nil

	case Tflush:
		return Rflush, nil, nil

	case Twalk:
		f, newfid, n := r.u32(), r.u32(), int(r.u16())
		names := make([]string, n)
		for i := range names {
			names[i] = r.s()
		}
		if r.bad {
			return Rerror, nil, errBadMsg
		}
		old, ok := fids[f]
		if !ok {
			return Rerror, nil, errNotExist
		}
		node := old.n
		walked := make([]*fsNode, 0, n)
		var werr error
		for _, name := range names {
			next, err := node.walk(name)
			if err != nil {
				werr = err
				break
			}
			node = next
			walked = append(walked, next)
		}
		if len(walked) == 0 && len(names) > 0 {
			return Rerror, nil, werr
		}
		if n == 0 {
			walked = nil
			node = old.n
		}
		fids[newfid] = &fid{n: node}
		w := &wbuf{}
		w.u16(uint16(len(walked)))
		for _, k := range walked {
			qid(w, k)
		}
		return Rwalk, w.b, nil

	case Topen:
		f, mode := r.u32(), r.u8()
		fd, ok := fids[f]
		if !ok {
			return Rerror, nil, errNotExist
		}
		if fd.open {
			return Rerror, nil, errors.New("already open")
		}
		if fd.n.dir {
			if mode&3 != OREAD {
				return Rerror, nil, errIsDir
			}
		} else {
			if fd.n.onLine == nil && (mode&3 == OWRITE || mode&3 == ORDWR) {
				return Rerror, nil, errPerm
			}
			if fd.n.onLine != nil && mode&3 == OREAD {
				return Rerror, nil, errPerm
			}
		}
		if mode&OTRUNC != 0 {
			if fd.n.onLine == nil {
				return Rerror, nil, errPerm
			}
			fd.wbuf = fd.wbuf[:0]
		}
		fd.open = true
		fd.mode = mode
		w := &wbuf{}
		qid(w, fd.n)
		w.u32(0) // iounit: use msize
		return Ropen, w.b, nil

	case Tread:
		f, off, cnt := r.u32(), r.u64(), r.u32()
		fd, ok := fids[f]
		if !ok {
			return Rerror, nil, errNotExist
		}
		if !fd.open {
			return Rerror, nil, errNotOpen
		}
		if fd.mode&3 != OREAD && fd.mode&3 != OEXEC && fd.mode&3 != ORDWR {
			return Rerror, nil, errPerm
		}
		if fd.n.dir {
			data := fd.n.dirData()
			if off >= uint64(len(data)) {
				w := &wbuf{}
				w.u32(0)
				return Rread, w.b, nil
			}
			data = data[off:]
			if uint64(cnt) < uint64(len(data)) {
				data = data[:cnt]
			}
			w := &wbuf{}
			w.u32(uint32(len(data)))
			w.b = append(w.b, data...)
			return Rread, w.b, nil
		}
		data := fd.n.content()
		if off > uint64(len(data)) {
			return Rerror, nil, errBadOffset
		}
		data = data[off:]
		max := uint64(*msize) - 11
		if uint64(cnt) > max {
			cnt = uint32(max)
		}
		if uint64(cnt) < uint64(len(data)) {
			data = data[:cnt]
		}
		w := &wbuf{}
		w.u32(uint32(len(data)))
		w.b = append(w.b, data...)
		return Rread, w.b, nil

	case Twrite:
		f, off, data := r.u32(), r.u64(), r.count()
		fd, ok := fids[f]
		if !ok {
			return Rerror, nil, errNotExist
		}
		if !fd.open || fd.n.onLine == nil {
			return Rerror, nil, errPerm
		}
		_ = off
		if len(data) > 0 {
			err := fd.write(data)
			if err != nil {
				return Rerror, nil, err
			}
		}
		w := &wbuf{}
		w.u32(uint32(len(data)))
		return Rwrite, w.b, nil

	case Tclunk:
		f := r.u32()
		if _, ok := fids[f]; !ok {
			return Rerror, nil, errNotExist
		}
		delete(fids, f)
		return Rclunk, nil, nil

	case Tstat:
		f := r.u32()
		fd, ok := fids[f]
		if !ok {
			return Rerror, nil, errNotExist
		}
		st := encodeStat(fd.n)
		w := &wbuf{}
		w.u16(uint16(len(st)))
		w.b = append(w.b, st...)
		return Rstat, w.b, nil

	case Tcreate, Tremove, Twstat:
		return Rerror, nil, errPerm
	}
	return Rerror, nil, fmt.Errorf("bad message type %d", typ)
}

func (f *fid) write(data []byte) error {
	f.wbuf = append(f.wbuf, data...)
	for {
		i := -1
		for j, c := range f.wbuf {
			if c == '\n' {
				i = j
				break
			}
		}
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(f.wbuf[:i]), "\r")
		f.wbuf = f.wbuf[i+1:]
		if err := f.n.onLine(line); err != nil {
			return err
		}
	}
	return nil
}
