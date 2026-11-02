// digfs serves a digup index as a 9P filesystem whose query results are
// directories of real files, and renders the same tree as HTML.
//
// It owns no index and no model: it asks `digup search-service` questions and
// makes the answers walkable.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

type rootsFlag []string

func (r *rootsFlag) String() string { return strings.Join(*r, ",") }
func (r *rootsFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
}

var verbose bool

// eventLog is a small ring the /log file reads: enough for an agent to see
// what happened without a tail on stderr.
var eventMu sync.Mutex
var eventLines []string

func eventf(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	eventMu.Lock()
	eventLines = append(eventLines, time.Now().Format("2006-01-02 15:04:05")+" "+msg)
	if len(eventLines) > 256 {
		eventLines = eventLines[len(eventLines)-256:]
	}
	eventMu.Unlock()
	logf("%s", msg)
}

func eventText() string {
	eventMu.Lock()
	defer eventMu.Unlock()
	if len(eventLines) == 0 {
		return ""
	}
	return strings.Join(eventLines, "\n") + "\n"
}

func defaultCache() string {
	if d, err := os.UserCacheDir(); err == nil {
		return d + "/digfs/fs"
	}
	return "/tmp/digfs-cache"
}

func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "digfs %s ", time.Now().Format("15:04:05"))
	fmt.Fprintf(os.Stderr, format, a...)
	fmt.Fprintln(os.Stderr)
}

func main() {
	var (
		addr     = flag.String("a", "tcp!127.0.0.1!5640", "9P address, net!host!port (host * for every interface)")
		webAddr  = flag.String("w", "", "HTML address, net!host!port; empty disables it")
		digup    = flag.String("digup", "digup", "the digup binary")
		index    = flag.String("index", "", "index folder (digup's default when empty)")
		models   = flag.String("models", "", "DIGUP_MODELS for the engine")
		clean    = flag.Bool("cleanenv", false, "run digup without LD_LIBRARY_PATH (when another llama.cpp shadows its own)")
		maxq     = flag.Int("maxq", 100, "most hits a single query may ask for")
		cache    = flag.String("cache", defaultCache(), "where mounted shares are mirrored")
		maxFetch = flag.Int64("maxfetch", 256<<20, "largest file a refresh will copy, in bytes")
	)
	var roots rootsFlag
	flag.Var(&roots, "root", "an indexed folder (repeatable; for the HTML view's file links)")
	flag.BoolVar(&verbose, "v", false, "log each connection")
	flag.Parse()

	if !strings.Contains(*digup, "/") {
		if p, err := exec.LookPath(*digup); err == nil {
			*digup = p
		}
	}

	// The engine answers only for the folders it was started with, so the
	// mirror has to be one of them: a mounted share is searchable from the
	// moment it is refreshed.
	roots = append(roots, *cache)

	eng := &engine{
		digup: *digup, index: *index, roots: roots, models: *models,
		cleanEnv: *clean, verbose: verbose,
	}
	t := &tree{eng: eng, roots: roots, maxQ: *maxq, started: time.Now(),
		shares: map[string]*share{}, remote: map[string]*fsNode{},
		cacheDir: *cache, maxFetch: *maxFetch}
	t.build()
	// A restart keeps the same namespace: re-dial every share we had.
	t.restoreShares()

	// The HTML view only serves files it can prove are indexed. Ask the engine
	// for the folders when none were named.
	if len(t.roots) == 0 {
		if s, err := eng.Summaries(); err == nil {
			for _, r := range s.Roots {
				t.roots = append(t.roots, r.Path)
			}
		}
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	go func() {
		s := &server{tree: t}
		if err := s.listen(*addr); err != nil {
			logf("9p: %v", err)
			sig <- syscall.SIGTERM
		}
	}()
	if *webAddr != "" {
		go func() {
			w := &web{t: t, roots: t.roots}
			if err := w.listen(*webAddr); err != nil {
				logf("web: %v", err)
			}
		}()
	}

	logf("index %s, %d roots", *index, len(t.roots))
	<-sig
	eng.stop()
}
