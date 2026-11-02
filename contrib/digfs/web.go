// The same tree as HTML.  Search is the front door: a box, the recent
// questions, and a page of cards per query.  A hit opens the passage it came
// from, rendered as Markdown when the file was one.
package main

import (
	_ "embed"
	"fmt"
	"html"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed mkd.js
var mkdJS []byte

type web struct {
	t     *tree
	roots []string
}

func (w *web) listen(a string) error {
	network, addr, err := plan9addr(a)
	if err != nil {
		return err
	}
	l, err := net.Listen(network, addr)
	if err != nil {
		return err
	}
	logf("web: listening on %s", l.Addr())
	srv := &http.Server{Handler: w.routes(), ReadHeaderTimeout: 10 * time.Second}
	return srv.Serve(l)
}

func (w *web) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", w.home)
	mux.HandleFunc("GET /search", w.search)
	mux.HandleFunc("POST /search", w.search)
	mux.HandleFunc("GET /q/{n}/", w.query)
	mux.HandleFunc("GET /q/{n}/{file}", w.passage)
	mux.HandleFunc("GET /q/{n}/paths", w.paths)
	mux.HandleFunc("GET /raw/{n}/{file}", w.raw)
	mux.HandleFunc("GET /mkd.js", w.markdownParser)
	mux.HandleFunc("GET /machines", w.machines)
	mux.HandleFunc("POST /mount", w.mount)
	mux.HandleFunc("GET /stats", w.stats)
	mux.HandleFunc("GET /help", w.help)
	return mux
}

const style = `
:root{
  --bg:#faf8f4; --fg:#1b1c1e; --muted:#71727a; --line:#e4e0d7;
  --card:#ffffff; --code:#f3f0e9; --accent:#2d6a4f; --accent-2:#e7f0ea;
  --shadow:0 1px 2px rgba(20,20,20,.05);
  --mono:ui-monospace,SFMono-Regular,"SF Mono",Menlo,Consolas,monospace;
  --sans:ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;
}
@media (prefers-color-scheme: dark){
  :root{
    --bg:#101113; --fg:#e9e7e2; --muted:#9a9aa1; --line:#26282e;
    --card:#17191d; --code:#0b0c0f; --accent:#8fd6ae; --accent-2:#1b2a23;
    --shadow:none;
  }
}
*{box-sizing:border-box}
html{color-scheme:light dark;-webkit-text-size-adjust:100%}
body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.65 var(--sans);
     min-height:100vh;display:flex;flex-direction:column}
a{color:var(--accent)}
.bar{position:sticky;top:0;z-index:5;display:flex;align-items:center;justify-content:space-between;
     padding:.65rem 1.25rem;background:var(--bg);border-bottom:1px solid var(--line)}
.brand{display:flex;align-items:center;gap:.55rem;font-weight:650;letter-spacing:-.01em;
       color:var(--fg);text-decoration:none}
.mark{width:.85rem;height:.85rem;border-radius:3px;background:var(--accent)}
.host{font:12px/1 var(--mono);color:var(--muted)}
.wrap{width:100%;max-width:56rem;margin:0 auto;padding:1.4rem 1.25rem 4rem;flex:1}
.crumbs{display:flex;flex-wrap:wrap;align-items:center;gap:.15rem;margin:.4rem 0 1rem;
        font:13px/1 var(--mono)}
.crumbs a,.crumbs span{color:var(--muted);text-decoration:none;padding:.3rem .4rem;border-radius:6px}
.crumbs a:hover{color:var(--accent);background:var(--accent-2)}
.head{display:flex;align-items:baseline;justify-content:space-between;gap:1rem;margin:.2rem 0 1rem}
.title{font:600 1.35rem/1.25 var(--mono);margin:0;letter-spacing:-.01em;overflow-wrap:anywhere}
.count{font:11px/1 var(--mono);color:var(--muted);text-transform:uppercase;letter-spacing:.06em;
       border:1px solid var(--line);border-radius:999px;padding:.35rem .6rem;white-space:nowrap}
.meta{color:var(--muted);font-size:.9rem}
.search{display:flex;gap:.5rem;margin:1.2rem 0 1.8rem;max-width:44rem}
.search input[type=search]{flex:1;padding:.7rem .9rem;border:1px solid var(--line);
       border-radius:10px;background:var(--card);color:var(--fg);font:15px var(--sans)}
.search input[type=search]:focus{outline:2px solid var(--accent);outline-offset:1px;
       border-color:transparent}
.hits{display:grid;gap:1rem}
.hit{margin:0;padding:1rem 1.1rem;background:var(--card);border:1px solid var(--line);
     border-radius:12px;box-shadow:var(--shadow)}
.hit .where{font:600 14px/1.5 var(--mono);overflow-wrap:anywhere}
.hit .where a{text-decoration:none}
.hit .where .share{color:var(--accent)}
.hit .where .path{color:var(--fg)}
.hit .chips{display:flex;flex-wrap:wrap;gap:.4rem;margin-top:.6rem}
.chip{font:11px/1 var(--mono);color:var(--muted);border:1px solid var(--line);
      border-radius:999px;padding:.35rem .6rem;white-space:nowrap}
.chip b{color:var(--fg);font-weight:600}
.hit .snippet{margin:.75rem 0 .9rem;font-size:.95rem;line-height:1.6;color:var(--fg);
             display:-webkit-box;-webkit-line-clamp:3;-webkit-box-orient:vertical;overflow:hidden}
.hit .more a{font:600 .85rem/1 var(--sans)}
.recent{margin:2.5rem 0 0}
.recent h2{font-size:.75rem;text-transform:uppercase;letter-spacing:.09em;color:var(--muted);
           margin:0 0 .5rem;font-weight:600}
.recent ul{list-style:none;padding:0;margin:0;border:1px solid var(--line);border-radius:12px;
           background:var(--card);overflow:hidden}
.recent li{border-top:1px solid var(--line)}
.recent li:first-child{border-top:0}
.recent a{display:block;padding:.6rem 1rem;text-decoration:none;color:var(--fg);
          font:13px/1.4 var(--mono);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.recent a:hover{background:var(--accent-2)}
.hero{padding:1.2rem 0 1.2rem}
.hero h1{font-size:1.7rem;line-height:1.2;margin:0 0 .4rem;letter-spacing:-.02em}
.hero p{margin:0;color:var(--muted)}
.doc{margin:1rem 0 0;padding:1.1rem 1.25rem;background:var(--code);border:1px solid var(--line);
     border-radius:12px;font:13.5px/1.7 var(--mono);white-space:pre-wrap;
     overflow-wrap:anywhere;tab-size:4}
.md{margin:1rem 0 0;padding:1.1rem 1.25rem;background:var(--code);border:1px solid var(--line);
    border-radius:12px;font:15px/1.65 var(--sans)}
.md > :first-child{margin-top:0}
.md > :last-child{margin-bottom:0}
.md h1,.md h2,.md h3,.md h4,.md h5,.md h6{font-family:var(--sans);line-height:1.3;
    margin:1.5rem 0 .5rem;letter-spacing:-.01em}
.md h1{font-size:1.4rem}
.md h2{font-size:1.15rem;padding-bottom:.3rem;border-bottom:1px solid var(--line)}
.md h3{font-size:1rem}
.md h4,.md h5,.md h6{font-size:.95rem}
.md p{margin:.7rem 0}
.md ul,.md ol{margin:.6rem 0;padding-left:1.4rem}
.md li{margin:.25rem 0}
.md blockquote{margin:.9rem 0;padding:.2rem 0 .2rem 1rem;border-left:3px solid var(--accent);
    color:var(--muted)}
.md code{font:12.5px/1.5 var(--mono);background:var(--card);border:1px solid var(--line);
    border-radius:5px;padding:.1rem .3rem}
.md pre.code{background:var(--card);border:1px solid var(--line);border-radius:8px;
    padding:.8rem 1rem;overflow:auto;margin:.9rem 0}
.md pre.code code{background:none;border:0;padding:0;font-size:12.5px;line-height:1.6}
.md a{color:var(--accent)}
.md hr{border:0;border-top:1px solid var(--line);margin:1.4rem 0}
.button{display:inline-block;padding:.5rem .8rem;border:0;border-radius:8px;
        background:var(--accent);color:#fff;font:600 .88rem/1 var(--sans);
        text-decoration:none;cursor:pointer}
.button.ghost{background:transparent;color:var(--accent);border:1px solid var(--accent)}
.foot{margin:3rem auto 2rem;max-width:56rem;padding:0 1.25rem;color:var(--muted);
      font:12px/1 var(--mono);display:flex;gap:.5rem;flex-wrap:wrap}
.empty{color:var(--muted);padding:2rem 0}
@media (max-width:40rem){
  .wrap{padding:1rem .8rem 3rem}
  .bar{padding:.55rem .8rem}
  .title{font-size:1.15rem}
  .search{flex-direction:column}
}
`

func (w *web) page(title, body string) string {
	return `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>` + html.EscapeString(title) + `</title>
<style>` + style + `</style></head>
<body>
<header class="bar">
  <a class="brand" href="/"><span class="mark"></span>digfs</a>
  <span class="host">` + html.EscapeString(hostname()) + `</span>
</header>
<main class="wrap">
` + body + `
</main>
<footer class="foot"><span>digfs</span><span>·</span><a href="/machines">machines</a><span>·</span><a href="/stats">stats</a><span>·</span><a href="/help">help</a></footer>
<script src="/mkd.js"></script>
<script>
(function(){
  if(typeof mkd === 'undefined') return;
  var list = document.querySelectorAll('pre.markdown');
  for(var i = 0; i < list.length; i++){
    var div = document.createElement('div');
    div.className = 'md';
    list[i].parentNode.insertBefore(div, list[i].nextSibling);
    list[i].style.display = 'none';
    new mkd(list[i], div).draw();
  }
})();
</script>
</body></html>`
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "digfs"
	}
	return h
}

func (w *web) home(rw http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString(`<section class="hero"><h1>Search the files on this machine by what's in them.</h1>`)
	b.WriteString(`<p>EmbeddingGemma 2 over the index; every hit opens the passage it came from.</p></section>`)
	b.WriteString(`<form class="search" action="/search" method="get">
<input type="search" name="q" placeholder="what are you looking for?" autofocus>
<button class="button" type="submit">search</button></form>`)
	w.recent(&b)
	b.WriteString(`<p class="meta" style="margin-top:2rem"><a href="/machines">Machines</a> — mount another host over 9P and it is indexed here too.</p>`)
	fmt.Fprint(rw, w.page("digfs", b.String()))
}

func (w *web) machines(rw http.ResponseWriter, r *http.Request) {
	w.t.mu.Lock()
	shares := make([]*share, 0, len(w.t.shares))
	for _, sh := range w.t.shares {
		shares = append(shares, sh)
	}
	w.t.mu.Unlock()

	var b strings.Builder
	b.WriteString(`<section class="hero"><h1>Machines</h1><p>A 9P export from another host, mirrored here and indexed into the same search. Results say which machine they came from.</p></section>`)

	if len(shares) == 0 {
		b.WriteString(`<p class="empty">No machine is mounted yet.</p>`)
	} else {
		b.WriteString(`<div class="hits">`)
		for _, sh := range shares {
			sh.mu.Lock()
			name, addr, aname := sh.name, sh.addr, sh.aname
			files, bytes, removed, state := sh.files, sh.bytes, sh.removed, sh.state
			last, lastIndex, serr := sh.lastRefresh, sh.lastIndex, sh.err
			sh.mu.Unlock()
			fmt.Fprintf(&b, `<article class="hit"><div class="where"><span class="share">%s</span> <span class="path">%s %s</span></div>`,
				html.EscapeString(name), html.EscapeString(addr), html.EscapeString(aname))
			b.WriteString(`<div class="chips">`)
			fmt.Fprintf(&b, `<span class="chip"><b>%d</b> files</span>`, files)
			fmt.Fprintf(&b, `<span class="chip">%d bytes</span>`, bytes)
			if removed > 0 {
				fmt.Fprintf(&b, `<span class="chip">%d removed</span>`, removed)
			}
			fmt.Fprintf(&b, `<span class="chip">%s</span>`, html.EscapeString(state))
			if !last.IsZero() {
				fmt.Fprintf(&b, `<span class="chip">refreshed %s</span>`, last.Format("15:04"))
			}
			if !lastIndex.IsZero() {
				fmt.Fprintf(&b, `<span class="chip">indexed %s</span>`, lastIndex.Format("15:04"))
			}
			b.WriteString(`</div>`)
			if serr != "" {
				fmt.Fprintf(&b, `<p class="snippet">%s</p>`, html.EscapeString(serr))
			}
			b.WriteString(`<p class="more"><a href="/stats">details</a></p></article>`)
		}
		b.WriteString(`</div>`)
	}

	b.WriteString(`<section class="recent"><h2>Mount a machine</h2>
<form class="search" action="/mount" method="post" style="display:grid;gap:.5rem;max-width:44rem">
<input type="text" name="name" placeholder="name (papers)" required>
<input type="text" name="addr" placeholder="tcp!host!5640" required>
<input type="text" name="path" placeholder="/export/path (optional)">
<button class="button" type="submit" style="justify-self:start">mount and index</button>
</form>
<p class="meta">The host runs a 9P server (another digfs, a 9front export, or <code>exportfs</code>).
digfs copies changed files into its cache and runs the indexer over them; nothing on the other
machine is written.</p></section>`)
	fmt.Fprint(rw, w.page("Machines", b.String()))
}

func (w *web) mount(rw http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	addr := strings.TrimSpace(r.PostFormValue("addr"))
	aname := strings.TrimSpace(r.PostFormValue("path"))
	if name == "" || addr == "" {
		http.Redirect(rw, r, "/machines", http.StatusSeeOther)
		return
	}
	sh, err := w.t.mount(name, addr, aname)
	if err != nil {
		eventf("mount %s: %v", name, err)
		http.Redirect(rw, r, "/machines", http.StatusSeeOther)
		return
	}
	go func() {
		if _, _, _, err := sh.refresh("", w.t.maxFetch, skipDirs); err != nil {
			eventf("mount %s: %v", name, err)
			return
		}
		if err := w.t.indexShare(name, ""); err != nil {
			eventf("mount %s: %v", name, err)
		}
	}()
	http.Redirect(rw, r, "/machines", http.StatusSeeOther)
}

func (w *web) search(rw http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		r.ParseForm()
	}
	q := strings.TrimSpace(r.FormValue("q"))
	code := r.FormValue("code") != ""
	if q == "" {
		http.Redirect(rw, r, "/", http.StatusSeeOther)
		return
	}
	if err := w.t.runQuery(q, code, 20, ""); err != nil {
		// The query is kept even when it failed, so the page can say why.
		logf("search %q: %v", q, err)
	}
	w.t.mu.Lock()
	n := w.t.nextQ
	w.t.mu.Unlock()
	http.Redirect(rw, r, fmt.Sprintf("/q/%d/", n), http.StatusSeeOther)
}

func (w *web) queryRec(n int) *queryRec {
	w.t.mu.Lock()
	defer w.t.mu.Unlock()
	for _, q := range w.t.queries {
		if q.n == n {
			return q
		}
	}
	return nil
}

func (w *web) query(rw http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.PathValue("n"))
	q := w.queryRec(n)
	if q == nil {
		http.NotFound(rw, r)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<div class="head"><h1 class="title">%s</h1><span class="count">%d hits</span></div>`,
		html.EscapeString(q.text), len(q.hits))
	fmt.Fprintf(&b, `<form class="search" action="/search" method="get">
<input type="search" name="q" value="%s" placeholder="search">
<button class="button" type="submit">search</button></form>`, html.EscapeString(q.text))
	if q.err != "" {
		fmt.Fprintf(&b, `<p class="meta">the search failed: %s</p>`, html.EscapeString(q.err))
	} else if q.meaning {
		fmt.Fprintf(&b, `<p class="meta">strong %d · %.0f ms embed, %.0f ms search</p>`,
			q.strong, q.embedMS, q.searchMS)
	}
	b.WriteString(`<div class="hits">`)
	for _, h := range q.hits {
		name := h.fileName()
		fmt.Fprintf(&b, `<article class="hit"><div class="where"><a href="/q/%d/%s">`, q.n, name)
		if h.shareName != "" {
			fmt.Fprintf(&b, `<span class="share">%s</span><span class="path">%s</span>`,
				html.EscapeString(h.shareName), html.EscapeString(h.remotePath))
		} else {
			fmt.Fprintf(&b, `<span class="path">%s</span>`, html.EscapeString(h.path))
		}
		b.WriteString(`</a></div><div class="chips">`)
		fmt.Fprintf(&b, `<span class="chip"><b>unit</b> %s %s</span>`, html.EscapeString(h.segment), html.EscapeString(formatLoc(h.loc)))
		fmt.Fprintf(&b, `<span class="chip"><b>score</b> %.2f</span>`, h.score)
		if h.kind != "" {
			fmt.Fprintf(&b, `<span class="chip">%s</span>`, html.EscapeString(h.kind))
		}
		b.WriteString(`</div>`)
		if s := plain(h.excerpt); s != "" {
			fmt.Fprintf(&b, `<p class="snippet">%s</p>`, highlight(s))
		}
		fmt.Fprintf(&b, `<p class="more"><a href="/q/%d/%s">Read the passage</a></p></article>`, q.n, name)
	}
	b.WriteString(`</div>`)
	if len(q.hits) == 0 && q.err == "" {
		b.WriteString(`<p class="empty">Nothing matched. Try fewer words, or a broader phrase.</p>`)
	}
	w.recent(&b)
	fmt.Fprint(rw, w.page(q.text, b.String()))
}

func (w *web) passage(rw http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.PathValue("n"))
	q := w.queryRec(n)
	if q == nil {
		http.NotFound(rw, r)
		return
	}
	name := r.PathValue("file")
	h := q.hitByName(name)
	if h == nil {
		http.NotFound(rw, r)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<nav class="crumbs"><a href="/q/%d/">results</a><span>/</span><span>%s</span></nav>`,
		q.n, html.EscapeString(filepath.Base(h.path)))
	fmt.Fprintf(&b, `<div class="head"><h1 class="title">%s</h1><span>`, html.EscapeString(filepath.Base(h.path)))
	fmt.Fprintf(&b, `<a class="button ghost" href="/q/%d/">back to results</a> `, q.n)
	if w.allowed(h.path) {
		fmt.Fprintf(&b, `<a class="button ghost" href="/raw/%d/%s">download</a>`, q.n, name)
	}
	b.WriteString(`</span></div>`)

	b.WriteString(`<section class="hit"><div class="where">`)
	if h.shareName != "" {
		fmt.Fprintf(&b, `<span class="share">%s</span><span class="path">%s</span>`,
			html.EscapeString(h.shareName), html.EscapeString(h.remotePath))
	} else {
		fmt.Fprintf(&b, `<span class="path">%s</span>`, html.EscapeString(h.path))
	}
	b.WriteString(`</div><div class="chips">`)
	fmt.Fprintf(&b, `<span class="chip"><b>unit</b> %s %s (segment %d)</span>`, html.EscapeString(h.segment), html.EscapeString(formatLoc(h.loc)), h.segID)
	fmt.Fprintf(&b, `<span class="chip"><b>score</b> %.4f</span>`, h.score)
	fmt.Fprintf(&b, `<span class="chip"><b>cosine</b> %.4f</span>`, h.cosine)
	if h.kind != "" {
		fmt.Fprintf(&b, `<span class="chip">%s</span>`, html.EscapeString(h.kind))
	}
	if !h.modified.IsZero() {
		fmt.Fprintf(&b, `<span class="chip">%s</span>`, html.EscapeString(h.modified.Format("2006-01-02 15:04")))
	}
	b.WriteString(`</div></section>`)

	class := "doc"
	if strings.HasSuffix(strings.ToLower(h.path), ".md") || strings.HasSuffix(strings.ToLower(h.path), ".markdown") {
		class = "doc markdown"
	}
	fmt.Fprintf(&b, `<pre class="%s">%s</pre>`, class, html.EscapeString(h.text))
	fmt.Fprint(rw, w.page(filepath.Base(h.path), b.String()))
}

func (w *web) paths(rw http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.PathValue("n"))
	q := w.queryRec(n)
	if q == nil {
		http.NotFound(rw, r)
		return
	}
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(rw, q.pathsText())
}

func (w *web) raw(rw http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.PathValue("n"))
	q := w.queryRec(n)
	if q == nil {
		http.NotFound(rw, r)
		return
	}
	h := q.hitByName(r.PathValue("file"))
	if h == nil || !w.allowed(h.path) {
		http.NotFound(rw, r)
		return
	}
	f, err := os.Open(h.path)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(rw, r)
		return
	}
	if ct := contentType(h.path); ct != "" {
		rw.Header().Set("Content-Type", ct)
	}
	http.ServeContent(rw, r, filepath.Base(h.path), st.ModTime(), f)
}

func (w *web) allowed(path string) bool {
	p, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, root := range w.roots {
		r, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if p == r || strings.HasPrefix(p, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (w *web) stats(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(rw, w.t.statsText())
	fmt.Fprint(rw, "\n")
	fmt.Fprint(rw, w.t.rootsText())
}

func (w *web) help(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(rw, helpText)
}

func (w *web) markdownParser(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	rw.Write(mkdJS)
}

// recent lists the last few questions, newest first, without repeats.
func (w *web) recent(b *strings.Builder) {
	w.t.mu.Lock()
	qs := append([]*queryRec(nil), w.t.queries...)
	w.t.mu.Unlock()
	seen := map[string]bool{}
	var lines []string
	for i := len(qs) - 1; i >= 0 && len(lines) < 8; i-- {
		q := qs[i]
		if seen[q.text] {
			continue
		}
		seen[q.text] = true
		lines = append(lines, fmt.Sprintf(`<li><a href="/q/%d/">%s</a></li>`, q.n, html.EscapeString(q.text)))
	}
	if len(lines) == 0 {
		return
	}
	b.WriteString(`<section class="recent"><h2>Recent searches</h2><ul>`)
	b.WriteString(strings.Join(lines, ""))
	b.WriteString(`</ul></section>`)
}

// highlight turns the engine's «matched words» into bold, escaping the rest.
func highlight(s string) string {
	const open, close = "\u00ab", "\u00bb" // the engine's match markers
	var b strings.Builder
	for {
		i := strings.Index(s, open)
		if i < 0 {
			b.WriteString(html.EscapeString(s))
			return b.String()
		}
		b.WriteString(html.EscapeString(s[:i]))
		s = s[i+len(open):]
		j := strings.Index(s, close)
		if j < 0 {
			b.WriteString(html.EscapeString(open + s))
			return b.String()
		}
		b.WriteString("<b>" + html.EscapeString(s[:j]) + "</b>")
		s = s[j+len(close):]
	}
}

// plain strips the Markdown markers a preview should not show.
func plain(s string) string {
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "`", "")
	r := strings.NewReplacer("# ", "", "## ", "", "### ", "")
	s = r.Replace(s)
	for strings.HasPrefix(s, "> ") {
		s = strings.TrimPrefix(s, "> ")
	}
	return strings.Join(strings.Fields(s), " ")
}

func contentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".mp4":
		return "video/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	}
	return ""
}
