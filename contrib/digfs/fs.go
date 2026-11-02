// The namespace: a small read-only tree whose interesting directory is a
// query result. Results are real files, so the whole Plan 9 toolbox works on
// them, and every result carries the path of the file it came from.
package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type fsNode struct {
	id      uint64
	name    string
	dir     bool
	mode    uint32
	mtime   uint32
	parent  *fsNode
	content func() []byte
	size    func() uint64
	reader  func(off uint64, count uint32) ([]byte, error)
	list    func() []*fsNode
	onLine  func(string) error
}

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
	for _, c := range n.list() {
		if c.name == name {
			return c, nil
		}
	}
	return nil, errNotExist
}

// dirSlice returns whole directory entries starting at byte offset off,
// never splitting one across reads.  A server that slices at an arbitrary
// byte makes the client fail with "malformed directory contents".
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

func (n *fsNode) stat() (name string, mode uint32, length uint64, mtime uint32) {
	length = 0
	if !n.dir {
		switch {
		case n.size != nil:
			length = n.size()
		case n.content != nil:
			length = uint64(len(n.content()))
		}
	}
	return n.name, n.mode, length, n.mtime
}

func encodeStat(n *fsNode) []byte {
	w := &wbuf{}
	w.u16(0) // size, filled below
	w.u16(0) // type
	w.u32(0) // dev
	qid(w, n)
	_, mode, length, mtime := n.stat()
	w.u32(mode)
	w.u32(mtime)
	w.u32(mtime)
	w.u64(length)
	w.s(n.name)
	w.s("digup")
	w.s("digup")
	w.s("digup")
	binary.LittleEndian.PutUint16(w.b[0:2], uint16(len(w.b)-2))
	return w.b
}

// tree holds the root and the queries made so far.
type tree struct {
	eng     *engine
	roots   []string
	mu      sync.Mutex
	nextID  uint64
	nextQ   int
	queries []*queryRec
	root    *fsNode
	qdir    *fsNode
	newest  *fsNode
	started time.Time
	maxQ    int

	shares   map[string]*share
	remote   map[string]*fsNode
	remoteMu sync.Mutex
	cacheDir string
	maxFetch int64
}

type queryRec struct {
	n        int
	text     string
	code     bool
	when     time.Time
	hits     []*hit
	strong   int
	meaning  bool
	embedMS  float64
	searchMS float64
	err      string
	node     *fsNode
}

type hit struct {
	idx      int
	path     string
	kind     string
	segment  string
	loc      float64
	segID    int
	score    float64
	cosine   float64
	z        float64
	kword    int
	size     int64
	modified time.Time
	excerpt  string
	text     string

	shareName  string // set when the file came through a mount
	remotePath string
	cachePath  string
}

func (t *tree) newID() uint64 { return atomic.AddUint64(&t.nextID, 1) }

func (t *tree) file(parent *fsNode, name string, mode uint32, content func() []byte) *fsNode {
	if mode == 0 {
		mode = 0444
	}
	return &fsNode{id: t.newID(), name: name, mode: mode, parent: parent,
		mtime: uint32(time.Now().Unix()), content: content}
}

func (t *tree) dirNode(parent *fsNode, name string, list func() []*fsNode) *fsNode {
	return &fsNode{id: t.newID(), name: name, dir: true, mode: DMDIR | 0555, parent: parent,
		mtime: uint32(time.Now().Unix()), list: list}
}

func (t *tree) build() *fsNode {
	t.root = &fsNode{id: t.newID(), name: "/", dir: true, mode: DMDIR | 0555, mtime: uint32(time.Now().Unix())}

	ctl := &fsNode{id: t.newID(), name: "ctl", mode: 0222, parent: t.root,
		mtime: uint32(time.Now().Unix()), onLine: t.control}
	stats := t.file(t.root, "stats", 0444, func() []byte { return []byte(t.statsText()) })
	help := t.file(t.root, "help", 0444, func() []byte { return []byte(helpText) })
	roots := t.file(t.root, "roots", 0444, func() []byte { return []byte(t.rootsText()) })
	shares := t.file(t.root, "shares", 0444, func() []byte { return []byte(t.sharesText()) })
	logFile := t.file(t.root, "log", 0444, func() []byte { return []byte(eventText()) })

	// mnt is the live view of every mounted server: a remote tree walked
	// through this namespace, not a copy.
	mnt := t.dirNode(t.root, "mnt", func() []*fsNode {
		t.mu.Lock()
		defer t.mu.Unlock()
		out := make([]*fsNode, 0, len(t.shares))
		for _, sh := range t.shares {
			out = append(out, t.remoteNode(sh, nil, sh.rootinfo))
		}
		return out
	})

	// q/newest always names the last query, without the numbering.
	t.newest = t.dirNode(nil, "newest", func() []*fsNode {
		t.mu.Lock()
		defer t.mu.Unlock()
		if n := len(t.queries); n > 0 {
			return t.queries[n-1].node.list()
		}
		return nil
	})

	t.qdir = t.dirNode(t.root, "q", func() []*fsNode {
		t.mu.Lock()
		defer t.mu.Unlock()
		out := make([]*fsNode, 0, len(t.queries)+1)
		out = append(out, t.newest)
		for _, r := range t.queries {
			out = append(out, r.node)
		}
		return out
	})
	t.newest.parent = t.qdir

	t.root.list = func() []*fsNode { return []*fsNode{ctl, stats, roots, shares, logFile, t.qdir, mnt, help} }
	return t.root
}

const helpText = `digfs - search results as files

	echo search power key long press >> ctl
	ls /q/newest
	cat /q/newest/00-pmic-bringup.md

Every hit is a file. Its first lines say where it came from:

	path: /home/me/notes/pmic.md
	unit: chunk 5 (segment 32)
	score: 6.1556 ...
	---
	<the text of that unit>

ctl commands:

	search <words>                 the ten best units in the whole index
	search <words> limit 25        more of them
	search <words> kind pdf,doc    only these kinds
	search <words> code            search the code index instead
	code <words>                   the same, shorter
	reload                         after an index run changed the index

	mount <name> <net!host!port> [path]
	                               mount a 9P server; it appears at /mnt/<name>
	umount <name>
	refresh <name> [path]          mirror remote files into the cache
	index <name> [path]            run the indexer over the mirror

q/ holds one directory per query, numbered in order; q/newest is the last one.
Each query directory has:

	query     the line you wrote
	summary   counts and timings
	paths     one source path per hit, for scripts
	NN-<name> one file per hit

The index and the model belong to digup; digfs only asks it questions. If a
file is not under an indexed folder, it will not appear here.

A mounted share is both things at once: /mnt/<name>/ is the live remote
tree, and refresh copies it into the cache so the indexer can read it. A
result from there names the file where it lives (share:/path) and says
where the copy it read is.

	share: papers
	path: papers:/2026/foo.pdf
	cache: /home/me/.cache/digfs/papers/2026/foo.pdf
`

func (t *tree) statsText() string {
	st, err := t.eng.Status()
	if err != nil {
		return fmt.Sprintf("error: %v\n", err)
	}
	t.mu.Lock()
	nq := len(t.queries)
	t.mu.Unlock()
	return fmt.Sprintf(`index: %s
encoder: %s
vectors: %d
files: %d
searchable: %d
pending: %d
unfinished: %d
code vectors: %d
queries: %d
shares: %d
`, st.Index, st.Encoder, st.Vectors, st.Files, st.Searchable, st.Pending,
		st.Unfinished, st.CodeVectors, nq, len(t.shares))
}

func (t *tree) rootsText() string {
	s, err := t.eng.Summaries()
	if err != nil {
		return fmt.Sprintf("error: %v\n", err)
	}
	var b strings.Builder
	for _, r := range s.Roots {
		fmt.Fprintf(&b, "%s\tfiles %d\tindexed %d\tpending %d\tfailed %d\n",
			r.Path, r.Files, r.Indexed, r.Pending, r.Failed)
	}
	return b.String()
}

// control is the write side: one command per line, buffered by the fid until
// the newline arrives.
func (t *tree) control(line string) error {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return nil
	}
	f := strings.Fields(line)
	switch f[0] {
	case "search", "code":
		code := f[0] == "code"
		limit := 10
		kinds := ""
		var q []string
		for i := 1; i < len(f); i++ {
			switch f[i] {
			case "limit":
				if i+1 < len(f) {
					if n, err := strconv.Atoi(f[i+1]); err == nil && n > 0 {
						limit = n
					}
					i++
				}
			case "kind", "kinds":
				if i+1 < len(f) {
					kinds = f[i+1]
					i++
				}
			case "code":
				code = true
			default:
				q = append(q, f[i])
			}
		}
		if len(q) == 0 {
			return fmt.Errorf("usage: search <words> [limit N] [kind a,b] [code]")
		}
		return t.runQuery(strings.Join(q, " "), code, limit, kinds)
	case "mount":
		if len(f) < 3 {
			return fmt.Errorf("usage: mount <name> <net!host!port> [path]")
		}
		aname := ""
		if len(f) > 3 {
			aname = strings.Join(f[3:], " ")
		}
		_, err := t.mount(f[1], f[2], aname)
		return err
	case "umount":
		if len(f) != 2 {
			return fmt.Errorf("usage: umount <name>")
		}
		return t.umount(f[1])
	case "refresh":
		if len(f) < 2 {
			return fmt.Errorf("usage: refresh <name> [path]")
		}
		sub := ""
		if len(f) > 2 {
			sub = strings.Join(f[2:], " ")
		}
		t.mu.Lock()
		sh, ok := t.shares[f[1]]
		t.mu.Unlock()
		if !ok {
			return fmt.Errorf("%s is not mounted", f[1])
		}
		go func() {
			if _, _, _, err := sh.refresh(sub, t.maxFetch, skipDirs); err != nil {
				eventf("refresh %s: %v", sh.name, err)
			}
		}()
		return nil
	case "index":
		if len(f) < 2 {
			return fmt.Errorf("usage: index <name> [path]")
		}
		sub := ""
		if len(f) > 2 {
			sub = strings.Join(f[2:], " ")
		}
		return t.indexShare(f[1], sub)
	case "reload":
		return t.eng.Reload()
	case "quit":
		go func() { time.Sleep(100 * time.Millisecond); os.Exit(0) }()
		return nil
	}
	return fmt.Errorf("unknown ctl command %q", f[0])
}

func (t *tree) runQuery(text string, code bool, limit int, kinds string) error {
	if t.maxQ > 0 && limit > t.maxQ {
		limit = t.maxQ
	}
	rep, err := t.eng.Search(text, code, limit, kinds)

	r := &queryRec{text: text, code: code, when: time.Now()}
	if err != nil {
		r.err = err.Error()
	} else {
		r.strong = rep.Strong
		r.meaning = rep.Meaning
		r.embedMS = rep.MS.Embed
		r.searchMS = rep.MS.Search
		for i, h := range rep.Hits {
			hi := &hit{
				idx: i, path: h.Path, kind: h.Kind, segment: h.Segment, loc: h.Loc,
				segID: h.SegmentID, score: h.Score, cosine: h.Cosine, z: h.Z,
				kword: h.KeywordRank, size: h.Size,
				modified: time.Unix(int64(h.Modified), 0),
				excerpt:  h.Excerpt,
			}
			if txt, err := t.eng.Text(h.SegmentID, code); err == nil && txt != "" {
				hi.text = txt
			}
			if hi.text == "" {
				hi.text = hi.excerpt
			}
			if name, remote, cache, ok := t.locate(h.Path); ok {
				hi.shareName = name
				hi.remotePath = remote
				hi.cachePath = cache
			}
			r.hits = append(r.hits, hi)
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextQ++
	r.n = t.nextQ
	r.node = t.queryNode(r)
	t.queries = append(t.queries, r)
	if len(t.queries) > 32 {
		t.queries = t.queries[len(t.queries)-32:]
	}
	return err
}

func (t *tree) queryNode(r *queryRec) *fsNode {
	n := &fsNode{
		id: t.newID(), name: fmt.Sprintf("%03d", r.n), dir: true,
		mode: DMDIR | 0555, mtime: uint32(r.when.Unix()), parent: t.qdir,
	}
	kids := []*fsNode{
		t.file(n, "query", 0444, func() []byte { return []byte(r.text + "\n") }),
		t.file(n, "summary", 0444, func() []byte { return []byte(r.summaryText()) }),
		t.file(n, "paths", 0444, func() []byte { return []byte(r.pathsText()) }),
	}
	for _, h := range r.hits {
		h := h
		kids = append(kids, t.file(n, h.fileName(), 0444, func() []byte { return h.content() }))
	}
	n.list = func() []*fsNode { return kids }
	return n
}

func (r *queryRec) summaryText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "query: %s\n", r.text)
	if r.code {
		b.WriteString("index: code\n")
	}
	fmt.Fprintf(&b, "when: %s\n", r.when.Format("2006-01-02 15:04:05"))
	if r.err != "" {
		fmt.Fprintf(&b, "error: %s\n", r.err)
		return b.String()
	}
	fmt.Fprintf(&b, "hits: %d (strong %d)\n", len(r.hits), r.strong)
	fmt.Fprintf(&b, "meaning: %v\n", r.meaning)
	fmt.Fprintf(&b, "embed: %.0f ms, search: %.0f ms\n", r.embedMS, r.searchMS)
	return b.String()
}

func (r *queryRec) summaryTiming() string {
	if r.err != "" {
		return "error"
	}
	return fmt.Sprintf("%.0f ms embed, %.0f ms search", r.embedMS, r.searchMS)
}

func (r *queryRec) hitByName(name string) *hit {
	for _, h := range r.hits {
		if h.fileName() == name {
			return h
		}
	}
	return nil
}

func (r *queryRec) pathsText() string {
	var b strings.Builder
	for _, h := range r.hits {
		fmt.Fprintf(&b, "%s\n", h.displayPath())
	}
	return b.String()
}

func (h *hit) fileName() string {
	base := filepath.Base(h.path)
	base = strings.Map(func(c rune) rune {
		if c < 32 || c == '/' || c == '\'' || c == '"' {
			return '_'
		}
		return c
	}, base)
	if base == "" || base == "." {
		base = "hit"
	}
	if len(base) > 64 {
		base = base[:64]
	}
	return fmt.Sprintf("%02d-%s", h.idx, base)
}

func (h *hit) displayPath() string {
	if h.shareName != "" {
		return h.shareName + ":" + h.remotePath
	}
	return h.path
}

func (h *hit) content() []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "path: %s\n", h.displayPath())
	if h.shareName != "" {
		fmt.Fprintf(&b, "share: %s\n", h.shareName)
		fmt.Fprintf(&b, "cache: %s\n", h.cachePath)
	}
	fmt.Fprintf(&b, "kind: %s\n", h.kind)
	fmt.Fprintf(&b, "unit: %s %s (segment %d)\n", h.segment, formatLoc(h.loc), h.segID)
	fmt.Fprintf(&b, "score: %.4f (cosine %.4f, z %.4f, keyword %d)\n", h.score, h.cosine, h.z, h.kword)
	if !h.modified.IsZero() {
		fmt.Fprintf(&b, "file: %d bytes, modified %s\n", h.size, h.modified.Format("2006-01-02 15:04"))
	}
	b.WriteString("---\n")
	b.WriteString(h.text)
	if !strings.HasSuffix(h.text, "\n") {
		b.WriteString("\n")
	}
	return []byte(b.String())
}

func formatLoc(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}
