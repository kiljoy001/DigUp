// The engine is `digup search-service`: one JSON request per line on stdin,
// one JSON reply per line on stdout, with the index open and the encoder warm.
// It is the seam the app itself uses on Windows and Linux, so digfs does not
// parse human output and does not open the SQLite file.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type engine struct {
	digup    string
	index    string
	roots    []string
	models   string
	cleanEnv bool
	verbose  bool

	mu     sync.Mutex
	cmd    *exec.Cmd
	in     *bufio.Writer
	out    chan []byte
	nextID int
}

type statusReply struct {
	Index       string `json:"index"`
	Encoder     string `json:"encoder"`
	Vectors     int    `json:"vectors"`
	Files       int    `json:"files"`
	Searchable  int    `json:"searchable"`
	Pending     int    `json:"pending"`
	Unfinished  int    `json:"unfinished"`
	CodeVectors int    `json:"code_vectors"`
}

type searchReply struct {
	Hits    []hitJSON `json:"hits"`
	Strong  int       `json:"strong"`
	Meaning bool      `json:"meaning"`
	MS      struct {
		Embed  float64 `json:"embed"`
		Search float64 `json:"search"`
	} `json:"ms"`
}

type hitJSON struct {
	Path        string         `json:"path"`
	Kind        string         `json:"kind"`
	Segment     string         `json:"segment"`
	Loc         float64        `json:"loc"`
	SegmentID   int            `json:"segment_id"`
	Score       float64        `json:"score"`
	Cosine      float64        `json:"cosine"`
	Z           float64        `json:"z"`
	KeywordRank int            `json:"keyword_rank"`
	Excerpt     string         `json:"excerpt"`
	Size        int64          `json:"size"`
	Modified    float64        `json:"modified"`
	Info        map[string]any `json:"info"`
}

type summariesReply struct {
	Roots []struct {
		Path    string `json:"path"`
		Files   int    `json:"files"`
		Indexed int    `json:"indexed"`
		Pending int    `json:"pending"`
		Failed  int    `json:"failed"`
		Skipped int    `json:"skipped"`
		Missing bool   `json:"missing"`
	} `json:"roots"`
}

func (e *engine) start() error {
	if e.cmd != nil {
		return nil
	}
	args := []string{"search-service"}
	args = append(args, e.roots...)
	if e.index != "" {
		args = append(args, "--index", e.index)
	}
	cmd := exec.Command(e.digup, args...)
	cmd.Stderr = os.Stderr
	if e.cleanEnv || e.models != "" {
		env := make([]string, 0, len(os.Environ())+1)
		for _, kv := range os.Environ() {
			if e.cleanEnv && strings.HasPrefix(kv, "LD_LIBRARY_PATH=") {
				continue
			}
			env = append(env, kv)
		}
		if e.models != "" {
			env = append(env, "DIGUP_MODELS="+e.models)
		}
		cmd.Env = env
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", e.digup, err)
	}
	e.cmd = cmd
	e.in = bufio.NewWriter(stdin)
	e.out = make(chan []byte, 8)
	go func(ch chan<- []byte, r *bufio.Scanner, c *exec.Cmd) {
		r.Buffer(make([]byte, 0, 1<<20), 64<<20)
		for r.Scan() {
			line := append([]byte(nil), r.Bytes()...)
			ch <- line
		}
		close(ch)
		c.Wait() // reap: the service may exit on its own after being idle
	}(e.out, bufio.NewScanner(stdout), cmd)
	if e.verbose {
		logf("engine: %s (pid %d)", e.digup, cmd.Process.Pid)
	}
	return nil
}

func (e *engine) stop() {
	if e.cmd == nil {
		return
	}
	e.request(map[string]any{"op": "quit"}, 2*time.Second)
	done := make(chan struct{})
	go func() { e.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		e.cmd.Process.Kill()
	}
	e.cmd = nil
}

// request sends one op and waits for the reply carrying its id, skipping the
// unsolicited {"event":...} lines the service writes while the encoder loads.
func (e *engine) request(req map[string]any, timeout time.Duration) ([]byte, error) {
	if err := e.start(); err != nil {
		return nil, err
	}
	e.nextID++
	want := e.nextID
	req["id"] = want

	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := e.in.Write(append(body, '\n')); err != nil {
		e.cmd = nil
		return nil, err
	}
	if err := e.in.Flush(); err != nil {
		e.cmd = nil
		return nil, err
	}

	deadline := time.After(timeout)
	for {
		select {
		case line, ok := <-e.out:
			if !ok {
				e.cmd = nil
				return nil, fmt.Errorf("the digup search service exited")
			}
			var probe struct {
				ID    *int   `json:"id"`
				Event string `json:"event"`
			}
			if json.Unmarshal(line, &probe) != nil || probe.ID == nil {
				continue // event line
			}
			if *probe.ID != want {
				continue
			}
			var reply struct {
				Error string `json:"error"`
			}
			json.Unmarshal(line, &reply)
			if reply.Error != "" {
				return nil, fmt.Errorf("%s", reply.Error)
			}
			return line, nil
		case <-deadline:
			return nil, fmt.Errorf("the digup search service did not answer within %s", timeout)
		}
	}
}

func (e *engine) call(req map[string]any, timeout time.Duration, out any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	line, err := e.request(req, timeout)
	if err != nil {
		return err
	}
	if out != nil {
		if err := json.Unmarshal(line, out); err != nil {
			return err
		}
	}
	return nil
}

func (e *engine) Status() (*statusReply, error) {
	var r statusReply
	err := e.call(map[string]any{"op": "status"}, 30*time.Second, &r)
	return &r, err
}

func (e *engine) Search(q string, code bool, limit int, kinds string) (*searchReply, error) {
	req := map[string]any{"op": "search", "q": q, "limit": limit, "code": code, "meaning": true}
	if kinds != "" {
		req["kinds"] = splitComma(kinds)
	}
	var r searchReply
	if err := e.call(req, 5*time.Minute, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (e *engine) Text(segment int, code bool) (string, error) {
	var r struct {
		Text *string `json:"text"`
	}
	if err := e.call(map[string]any{"op": "text", "segment": segment, "code": code},
		60*time.Second, &r); err != nil {
		return "", err
	}
	if r.Text == nil {
		return "", nil
	}
	return *r.Text, nil
}

func (e *engine) Reload() error {
	return e.call(map[string]any{"op": "reload"}, 5*time.Minute, nil)
}

func (e *engine) Summaries() (*summariesReply, error) {
	var r summariesReply
	err := e.call(map[string]any{"op": "summaries", "code": false}, 60*time.Second, &r)
	return &r, err
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}
