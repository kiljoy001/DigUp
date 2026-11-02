// A 9P2000 client: enough to walk a foreign namespace and read its files.
//
// Only the operations a reader needs — version, attach, walk, open, read,
// clunk — and one request in flight at a time, which is all a crawler uses.
package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

type p9client struct {
	conn  net.Conn
	msize uint32
	tag   uint16
	fid   uint32
}

type p9dir struct {
	name   string
	qtype  uint8
	qpath  uint64
	mode   uint32
	atime  uint32
	mtime  uint32
	length uint64
}

func (d p9dir) isDir() bool { return d.mode&DMDIR != 0 || d.qtype&QTDIR != 0 }

// p9dial connects to a 9P server and negotiates 9P2000.
func p9dial(addr string) (*p9client, error) {
	network, hostport, err := plan9addr(addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout(network, hostport, 10*time.Second)
	if err != nil {
		return nil, err
	}
	c := &p9client{conn: conn, msize: 65536, fid: 0}
	w := &wbuf{}
	w.u32(c.msize)
	w.s("9P2000")
	typ, body, err := c.rpc(Tversion, w.b)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if typ != Rversion {
		conn.Close()
		return nil, fmt.Errorf("version: unexpected reply %d", typ)
	}
	r := &rbuf{b: body}
	msize := r.u32()
	ver := r.s()
	if ver != "9P2000" || r.bad {
		conn.Close()
		return nil, fmt.Errorf("server speaks %q, not 9P2000", ver)
	}
	if msize < c.msize {
		c.msize = msize
	}
	return c, nil
}

func (c *p9client) close() error { return c.conn.Close() }

func (c *p9client) nextfid() uint32 {
	c.fid++
	return c.fid
}

// rpc sends one message and waits for its reply. Serialized: a crawler does
// not need pipelining, and one tag at a time removes a whole class of bugs.
func (c *p9client) rpc(typ uint8, body []byte) (uint8, []byte, error) {
	c.tag++
	tag := c.tag
	w := &wbuf{}
	w.u32(0)
	w.u8(typ)
	w.u16(tag)
	w.b = append(w.b, body...)
	binary.LittleEndian.PutUint32(w.b, uint32(len(w.b)))
	if _, err := c.conn.Write(w.b); err != nil {
		return 0, nil, err
	}
	c.conn.SetReadDeadline(time.Now().Add(120 * time.Second))
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c.conn, hdr); err != nil {
		return 0, nil, err
	}
	size := binary.LittleEndian.Uint32(hdr)
	if size < 7 || size > 4<<20 {
		return 0, nil, fmt.Errorf("bad reply size %d", size)
	}
	msg := make([]byte, size)
	copy(msg, hdr)
	if _, err := io.ReadFull(c.conn, msg[4:]); err != nil {
		return 0, nil, err
	}
	c.conn.SetReadDeadline(time.Time{})
	if rtag := binary.LittleEndian.Uint16(msg[5:7]); rtag != tag {
		return 0, nil, fmt.Errorf("reply tag %d, want %d", rtag, tag)
	}
	rtyp := msg[4]
	body = msg[7:]
	if rtyp == Rerror {
		r := &rbuf{b: body}
		return rtyp, nil, errors.New(r.s())
	}
	return rtyp, body, nil
}

func (c *p9client) attach(uname, aname string) (uint32, p9dir, error) {
	fid := c.nextfid()
	w := &wbuf{}
	w.u32(fid)
	w.u32(NOFID)
	w.s(uname)
	w.s(aname)
	typ, body, err := c.rpc(Tattach, w.b)
	if err != nil {
		return 0, p9dir{}, err
	}
	if typ != Rattach {
		return 0, p9dir{}, fmt.Errorf("attach: unexpected reply %d", typ)
	}
	r := &rbuf{b: body}
	d := p9dir{qtype: r.u8(), qpath: r.u64()}
	if r.bad {
		return 0, p9dir{}, errBadMsg
	}
	c.conn.SetReadDeadline(time.Time{})
	return fid, d, nil
}

func (c *p9client) walk(fid uint32, names []string) (uint32, error) {
	newfid := c.nextfid()
	w := &wbuf{}
	w.u32(fid)
	w.u32(newfid)
	w.u16(uint16(len(names)))
	for _, n := range names {
		w.s(n)
	}
	typ, body, err := c.rpc(Twalk, w.b)
	if err != nil {
		return 0, err
	}
	if typ != Rwalk {
		return 0, fmt.Errorf("walk: unexpected reply %d", typ)
	}
	r := &rbuf{b: body}
	n := int(r.u16())
	if r.bad {
		return 0, errBadMsg
	}
	if n != len(names) {
		// Partial walks leave an unnamed fid behind; give the caller what did
		// resolve and let it decide.
		if n == 0 {
			return 0, errNotExist
		}
		c.clunk(newfid)
		return 0, fmt.Errorf("%s: %w", strings.Join(names[n:], "/"), errNotExist)
	}
	for i := 0; i < n; i++ {
		r.u8()
		r.u32()
		r.u64()
	}
	return newfid, nil
}

func (c *p9client) open(fid uint32, mode uint8) error {
	w := &wbuf{}
	w.u32(fid)
	w.u8(mode)
	typ, _, err := c.rpc(Topen, w.b)
	if err != nil {
		return err
	}
	if typ != Ropen {
		return fmt.Errorf("open: unexpected reply %d", typ)
	}
	return nil
}

func (c *p9client) readAt(fid uint32, off uint64, count uint32) ([]byte, error) {
	if count > c.msize-11 {
		count = c.msize - 11
	}
	w := &wbuf{}
	w.u32(fid)
	w.u64(off)
	w.u32(count)
	typ, body, err := c.rpc(Tread, w.b)
	if err != nil {
		return nil, err
	}
	if typ != Rread {
		return nil, fmt.Errorf("read: unexpected reply %d", typ)
	}
	r := &rbuf{b: body}
	data := r.count()
	if r.bad {
		return nil, errBadMsg
	}
	return append([]byte(nil), data...), nil
}

func (c *p9client) clunk(fid uint32) {
	w := &wbuf{}
	w.u32(fid)
	c.rpc(Tclunk, w.b)
}

// stat reads the dir entry for a fid. Used for attach roots and for walking
// components the caller names without a preceding directory read.
func (c *p9client) stat(fid uint32) (p9dir, error) {
	w := &wbuf{}
	w.u32(fid)
	typ, body, err := c.rpc(Tstat, w.b)
	if err != nil {
		return p9dir{}, err
	}
	if typ != Rstat {
		return p9dir{}, fmt.Errorf("stat: unexpected reply %d", typ)
	}
	r := &rbuf{b: body}
	n := int(r.u16())
	if r.bad || n > len(r.b) {
		return p9dir{}, errBadMsg
	}
	d, _, err := parseDir(r.b[:n])
	return d, err
}

// parseDir decodes one stat structure (with its size prefix) and returns the
// number of bytes it occupied.
func parseDir(b []byte) (p9dir, int, error) {
	if len(b) < 2 {
		return p9dir{}, 0, errBadMsg
	}
	size := int(binary.LittleEndian.Uint16(b))
	total := 2 + size
	if total > len(b) {
		return p9dir{}, 0, errBadMsg
	}
	r := &rbuf{b: b[2:total]}
	d := p9dir{}
	r.u16() // type
	r.u32() // dev
	d.qtype = r.u8()
	r.u32() // vers
	d.qpath = r.u64()
	d.mode = r.u32()
	d.atime = r.u32()
	d.mtime = r.u32()
	d.length = r.u64()
	d.name = r.s()
	r.s() // uid
	r.s() // gid
	r.s() // muid
	if r.bad {
		return p9dir{}, 0, errBadMsg
	}
	return d, total, nil
}

// readdir lists a directory fid, which the caller must have opened.
func (c *p9client) readdir(fid uint32) ([]p9dir, error) {
	var out []p9dir
	var off uint64
	for {
		data, err := c.readAt(fid, off, c.msize-11)
		if err != nil {
			return nil, err
		}
		if len(data) == 0 {
			break
		}
		off += uint64(len(data))
		for len(data) > 0 {
			d, n, err := parseDir(data)
			if err != nil {
				return nil, err
			}
			out = append(out, d)
			data = data[n:]
		}
	}
	return out, nil
}
