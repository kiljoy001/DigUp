// Mounting: a foreign 9P tree becomes part of this namespace, and can be
// mirrored into a cache so the indexer (which reads local paths) can see it.
//
// The mirror is the only copy digfs makes, and it is keyed by the share name:
// a result then reports `papers:/2026/foo.pdf` while the bytes it read live
// under ~/.cache/digfs/fs/papers.  Delete the cache and the index still
// answers, because the text of every unit is in the index itself.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type share struct {
	t        *tree
	name     string
	addr     string
	aname    string
	c        *p9client
	root     uint32 // attached fid for aname
	rootinfo p9dir
	cache    string

	mu    sync.Mutex
	state string
	when  time.Time
	files int64
	bytes int64
	err   string
}

func (t *tree) mount(name, addr, aname string) (*share, error) {
	if name == "" || addr == "" {
		return nil, fmt.Errorf("usage: mount <name> <net!host!port> [path]")
	}
	if _, ok := t.shares[name]; ok {
		return nil, fmt.Errorf("%s is already mounted", name)
	}
	c, err := p9dial(addr)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", addr, err)
	}
	fid, q, err := c.attach("digfs", aname)
	if err != nil {
		c.close()
		return nil, fmt.Errorf("%s: attach %q: %w", addr, aname, err)
	}
	if q.qtype&QTDIR == 0 {
		c.close()
		return nil, fmt.Errorf("%s: %q is not a directory", addr, aname)
	}
	info, err := c.stat(fid)
	if err != nil {
		info = p9dir{name: name, mode: DMDIR | 0555, qtype: QTDIR}
	}
	info.name = name // the namespace name is ours, not the server's root name
	sh := &share{
		t: t, name: name, addr: addr, aname: aname, c: c, root: fid,
		rootinfo: info, cache: filepath.Join(t.cacheDir, name),
		state: "mounted", when: time.Now(),
	}
	if err := os.MkdirAll(sh.cache, 0777); err != nil {
		c.close()
		return nil, err
	}
	t.mu.Lock()
	t.shares[name] = sh
	t.mu.Unlock()
	logf("mount %s at %s (aname %q), cache %s", name, addr, aname, sh.cache)
	return sh, nil
}

func (t *tree) umount(name string) error {
	t.mu.Lock()
	sh, ok := t.shares[name]
	if ok {
		delete(t.shares, name)
	}
	t.mu.Unlock()
	if !ok {
		return fmt.Errorf("%s is not mounted", name)
	}
	sh.c.clunk(sh.root)
	sh.c.close()
	return nil
}

func (sh *share) comps(sub string) []string {
	sub = strings.Trim(sub, "/")
	if sub == "" || sub == "." {
		return nil
	}
	return strings.Split(sub, "/")
}

// walkPath resolves components below the attach point.
func (sh *share) walkPath(comps []string) (uint32, error) {
	// Always clone: the attached fid is shared, and callers clunk what they
	// are given.  A zero-name walk is how 9P clones a fid.
	return sh.c.walk(sh.root, comps)
}

func (sh *share) list(comps []string) ([]p9dir, error) {
	fid, err := sh.walkPath(comps)
	if err != nil {
		return nil, err
	}
	defer sh.c.clunk(fid)
	if err := sh.c.open(fid, OREAD); err != nil {
		return nil, err
	}
	return sh.c.readdir(fid)
}

func (sh *share) statPath(comps []string) (p9dir, error) {
	fid, err := sh.walkPath(comps)
	if err != nil {
		return p9dir{}, err
	}
	defer sh.c.clunk(fid)
	return sh.c.stat(fid)
}

// readAt reads a window of a remote file. Opening per read is wasteful for a
// sequential copy; refresh() uses copy() below instead.
func (sh *share) readAt(comps []string, off uint64, count uint32) ([]byte, error) {
	fid, err := sh.walkPath(comps)
	if err != nil {
		return nil, err
	}
	defer sh.c.clunk(fid)
	if err := sh.c.open(fid, OREAD); err != nil {
		return nil, err
	}
	return sh.c.readAt(fid, off, count)
}

func (sh *share) copy(comps []string, d p9dir, dest string, maxFile int64) (int64, bool, error) {
	if int64(d.length) > maxFile {
		return 0, false, nil
	}
	if st, err := os.Stat(dest); err == nil {
		if st.Size() == int64(d.length) && st.ModTime().Unix() == int64(d.mtime) {
			return 0, false, nil
		}
	}
	fid, err := sh.walkPath(comps)
	if err != nil {
		return 0, false, err
	}
	defer sh.c.clunk(fid)
	if err := sh.c.open(fid, OREAD); err != nil {
		return 0, false, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0777); err != nil {
		return 0, false, err
	}
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, false, err
	}
	var off uint64
	var total int64
	for {
		data, err := sh.c.readAt(fid, off, sh.c.msize-11)
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return 0, false, err
		}
		if len(data) == 0 {
			break
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			os.Remove(tmp)
			return 0, false, err
		}
		off += uint64(len(data))
		total += int64(len(data))
		if maxFile > 0 && total > maxFile {
			f.Close()
			os.Remove(tmp)
			return 0, false, fmt.Errorf("%s: larger than maxfetch", strings.Join(comps, "/"))
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return 0, false, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return 0, false, err
	}
	ts := time.Unix(int64(d.mtime), 0)
	os.Chtimes(dest, ts, ts)
	return total, true, nil
}

var skipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true, "node_modules": true,
}

// refresh mirrors sub (relative to the attach point) into the cache.  Files
// whose size and modification time already match are left alone.
func (sh *share) refresh(sub string, maxFile int64, skip map[string]bool) (int64, int64, error) {
	sh.mu.Lock()
	if sh.state == "refreshing" || sh.state == "indexing" {
		state := sh.state
		sh.mu.Unlock()
		return 0, 0, fmt.Errorf("%s is %s", sh.name, state)
	}
	sh.state = "refreshing"
	sh.mu.Unlock()
	defer func() {
		sh.mu.Lock()
		if sh.state == "refreshing" {
			sh.state = "mounted"
		}
		sh.mu.Unlock()
	}()

	type item struct {
		comps []string
		d     p9dir
	}
	root := sh.comps(sub)
	queue := []item{{root, p9dir{}}}
	var files, bytes int64
	var firstErr error
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		entries, err := sh.list(it.comps)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		rel := strings.TrimPrefix(strings.Join(it.comps, "/"), "/")
		for _, d := range entries {
			name := d.name
			if name == "." || name == ".." || name == "" {
				continue
			}
			comps := append(append([]string(nil), it.comps...), name)
			if d.isDir() {
				if skip[name] {
					continue
				}
				queue = append(queue, item{comps, d})
				continue
			}
			n, copied, err := sh.copy(comps, d, filepath.Join(sh.cache, rel, name), maxFile)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if copied {
				files++
				bytes += n
			}
		}
	}
	sh.mu.Lock()
	sh.files += files
	sh.bytes += bytes
	sh.when = time.Now()
	if firstErr != nil {
		sh.err = firstErr.Error()
		sh.state = "error"
	} else {
		sh.err = ""
	}
	sh.mu.Unlock()
	logf("refresh %s/%s: %d files, %d bytes", sh.name, sub, files, bytes)
	return files, bytes, firstErr
}

// indexShare runs `digup index` over the mirror in the background: an index
// of a real tree takes minutes, and a 9P write should not block on it.
func (t *tree) indexShare(name, sub string) error {
	t.mu.Lock()
	sh, ok := t.shares[name]
	t.mu.Unlock()
	if !ok {
		return fmt.Errorf("%s is not mounted", name)
	}
	dir := filepath.Join(sh.cache, strings.Trim(sub, "/"))
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fmt.Errorf("%s: nothing mirrored there yet; run refresh %s %s", dir, name, sub)
	}
	sh.mu.Lock()
	if sh.state == "indexing" {
		sh.mu.Unlock()
		return fmt.Errorf("%s is already indexing", name)
	}
	sh.state = "indexing"
	sh.mu.Unlock()

	go func() {
		args := []string{"index", dir}
		if t.eng.index != "" {
			args = append(args, "--index", t.eng.index)
		}
		cmd := t.eng.command(args...)
		log := ""
		logPath := filepath.Join(t.cacheDir, "index.log")
		if t.eng.index != "" {
			logPath = filepath.Join(t.eng.index, "digfs-index.log")
		}
		if f, err := os.Create(logPath); err == nil {
			cmd.Stdout = f
			cmd.Stderr = f
			defer f.Close()
		}
		err := cmd.Run()
		if err != nil {
			log = err.Error()
		}
		if err == nil {
			if rerr := t.eng.Reload(); rerr != nil {
				err = rerr
			}
		}
		sh.mu.Lock()
		sh.when = time.Now()
		if err != nil {
			sh.state = "error"
			sh.err = err.Error()
		} else {
			sh.state = "indexed"
			sh.err = ""
		}
		sh.mu.Unlock()
		if err != nil {
			logf("index %s: %v", name, err)
		} else {
			logf("index %s: done", name)
		}
		_ = log
	}()
	return nil
}

func (t *tree) sharesText() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var b strings.Builder
	for _, sh := range t.shares {
		sh.mu.Lock()
		fmt.Fprintf(&b, "%s\t%s\t%s\t%d files\t%d bytes\t%s\t%s\n",
			sh.name, sh.addr, sh.aname, sh.files, sh.bytes, sh.state, sh.cache)
		if sh.err != "" {
			fmt.Fprintf(&b, "\terror: %s\n", sh.err)
		}
		sh.mu.Unlock()
	}
	return b.String()
}

// locate maps a cached path back to the share it came from, so a result can
// name the file where it actually lives.
func (t *tree) locate(path string) (name, remote, cache string, ok bool) {
	p, err := filepath.Abs(path)
	if err != nil {
		return "", "", "", false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, sh := range t.shares {
		root, err := filepath.Abs(sh.cache)
		if err != nil {
			continue
		}
		if p != root && !strings.HasPrefix(p, root+string(filepath.Separator)) {
			continue
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			continue
		}
		remote = "/" + filepath.ToSlash(rel)
		if sh.aname != "" {
			remote = strings.TrimSuffix(sh.aname, "/") + remote
		}
		return sh.name, remote, p, true
	}
	return "", "", "", false
}

// remoteNode wraps a remote entry as a node in this namespace.  Nodes are
// reused per path so a walk and a later stat see the same qid.
func (t *tree) remoteNode(sh *share, comps []string, d p9dir) *fsNode {
	key := sh.name + "\x00" + strings.Join(comps, "/")
	t.remoteMu.Lock()
	if n, ok := t.remote[key]; ok {
		t.remoteMu.Unlock()
		return n
	}
	t.remoteMu.Unlock()

	name := d.name
	if name == "" && len(comps) > 0 {
		name = comps[len(comps)-1]
	}
	if name == "" {
		name = sh.name
	}
	n := &fsNode{id: t.newID(), name: name, mode: 0555, mtime: d.mtime}
	if d.isDir() {
		n.dir = true
		n.mode = DMDIR | 0555
		n.list = func() []*fsNode { return t.remoteChildren(sh, comps) }
	} else {
		length := d.length
		n.size = func() uint64 { return length }
		n.reader = func(off uint64, count uint32) ([]byte, error) {
			return sh.readAt(comps, off, count)
		}
	}
	t.remoteMu.Lock()
	if old, ok := t.remote[key]; ok {
		t.remoteMu.Unlock()
		return old
	}
	t.remote[key] = n
	t.remoteMu.Unlock()
	return n
}

func (t *tree) remoteChildren(sh *share, comps []string) []*fsNode {
	entries, err := sh.list(comps)
	if err != nil {
		logf("mount %s/%s: %v", sh.name, strings.Join(comps, "/"), err)
		return nil
	}
	out := make([]*fsNode, 0, len(entries))
	for _, d := range entries {
		if d.name == "." || d.name == ".." || d.name == "" {
			continue
		}
		child := append(append([]string(nil), comps...), d.name)
		out = append(out, t.remoteNode(sh, child, d))
	}
	return out
}
