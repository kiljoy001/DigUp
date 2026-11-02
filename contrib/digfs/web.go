// The same tree as HTML, for a person with a browser. This is the "webbox"
// side of the service: no separate application, just a rendering of the
// namespace a 9P client walks.
package main

import (
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
	mux.HandleFunc("GET /", w.index)
	mux.HandleFunc("GET /search", w.search)
	mux.HandleFunc("POST /search", w.search)
	mux.HandleFunc("GET /q/{n}/", w.query)
	mux.HandleFunc("GET /q/{n}/{file}", w.hitPage)
	mux.HandleFunc("GET /raw/{n}/{file}", w.raw)
	mux.HandleFunc("GET /stats", w.stats)
	mux.HandleFunc("GET /help", w.help)
	return mux
}

func (w *web) html(title, body string) string {
	return `<!doctype html>
<html><head><meta charset="utf-8"><title>` + html.EscapeString(title) + `</title>
<style>
 body{font:15px/1.5 system-ui,sans-serif;max-width:60rem;margin:2rem auto;padding:0 1rem;color:#ddd;background:#16181c}
 a{color:#8ec07c} a:visited{color:#8ec07c}
 pre{background:#0e1013;padding:1rem;overflow-x:auto;white-space:pre-wrap;word-wrap:break-word;color:#d8d8d8}
 form{margin:1.5rem 0} input[type=text]{width:70%;padding:.4rem;font-size:1rem;background:#0e1013;color:#eee;border:1px solid #333}
 .meta{color:#888} .hit{padding:.6rem 0;border-bottom:1px solid #262a30}
 code{background:#0e1013;padding:.1rem .25rem}
</style></head><body>` + body + `</body></html>`
}

func (w *web) index(rw http.ResponseWriter, r *http.Request) {
	w.t.mu.Lock()
	qs := append([]*queryRec(nil), w.t.queries...)
	w.t.mu.Unlock()

	var b strings.Builder
	b.WriteString(`<h1>digfs</h1><p class="meta">search results as files. The 9P tree is the real interface; this page renders the same directory.</p>`)
	b.WriteString(`<form action="/search" method="post"><input type="text" name="q" placeholder="words" autofocus>
	 <label><input type="checkbox" name="code" value="1"> code index</label>
	 <button>search</button></form>`)
	if len(qs) == 0 {
		b.WriteString(`<p class="meta">No queries yet.</p>`)
	} else {
		b.WriteString(`<h2>queries</h2><ul>`)
		for i := len(qs) - 1; i >= 0; i-- {
			q := qs[i]
			fmt.Fprintf(&b, `<li><a href="/q/%d/">%03d</a> %s <span class="meta">%d hits, %s</span></li>`,
				q.n, q.n, html.EscapeString(q.text), len(q.hits), q.when.Format("15:04:05"))
		}
		b.WriteString(`</ul>`)
	}
	fmt.Fprint(rw, w.html("digfs", b.String()))
}

func (w *web) search(rw http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		r.ParseForm()
	}
	q := strings.TrimSpace(r.FormValue("q"))
	if q == "" {
		http.Redirect(rw, r, "/", http.StatusSeeOther)
		return
	}
	code := r.FormValue("code") != ""
	if err := w.t.runQuery(q, code, 20, ""); err != nil {
		w.t.mu.Lock()
		n := w.t.nextQ
		w.t.mu.Unlock()
		http.Redirect(rw, r, fmt.Sprintf("/q/%d/", n), http.StatusSeeOther)
		return
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
	fmt.Fprintf(&b, `<h1>%s</h1><p class="meta">%s · %d hits`, html.EscapeString(q.text),
		html.EscapeString(q.summaryTiming()), len(q.hits))
	if q.code {
		b.WriteString(" · code index")
	}
	b.WriteString(`</p><p><a href="/">back</a> · <a href="/q/` + strconv.Itoa(q.n) + `/paths">paths</a></p>`)
	if q.err != "" {
		fmt.Fprintf(&b, `<pre>error: %s</pre>`, html.EscapeString(q.err))
	}
	for _, h := range q.hits {
		name := h.fileName()
		fmt.Fprintf(&b, `<div class="hit"><a href="/q/%d/%s">%s</a> <span class="meta">%s %s · score %.3f (cosine %.3f)</span><br>`,
			q.n, name, html.EscapeString(filepath.Base(h.path)), html.EscapeString(h.kind),
			html.EscapeString(formatLoc(h.loc)), h.score, h.cosine)
		fmt.Fprintf(&b, `<span class="meta">%s · <a href="/raw/%d/%s">open the file</a></span><pre>%s</pre></div>`,
			html.EscapeString(h.displayPath()), q.n, name, html.EscapeString(truncate(h.excerpt, 600)))
	}
	fmt.Fprint(rw, w.html(q.text, b.String()))
}

func (w *web) hitPage(rw http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.PathValue("n"))
	q := w.queryRec(n)
	if q == nil {
		http.NotFound(rw, r)
		return
	}
	name := r.PathValue("file")
	if name == "paths" {
		rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(rw, q.pathsText())
		return
	}
	h := q.hitByName(name)
	if h == nil {
		http.NotFound(rw, r)
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<h1>%s</h1><p class="meta">%s · unit %s %s · score %.3f (cosine %.3f)<br>%s · <a href="/raw/%d/%s">open the file</a> · <a href="/q/%d/">back</a></p>`,
		html.EscapeString(filepath.Base(h.path)), html.EscapeString(h.kind), html.EscapeString(h.segment),
		html.EscapeString(formatLoc(h.loc)), h.score, h.cosine, html.EscapeString(h.displayPath()), q.n, name, q.n)
	fmt.Fprintf(&b, `<pre>%s</pre>`, html.EscapeString(h.text))
	fmt.Fprint(rw, w.html(filepath.Base(h.path), b.String()))
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
