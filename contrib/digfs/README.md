# digfs — a digup index as a filesystem

digfs serves a DigUp index over 9P2000 so that **a query result is a
directory of real files**, each one carrying the path of the file it came
from. It owns no index and no model: it asks `digup search-service` (the
JSON-lines interface the app already uses on Windows and Linux) and makes the
answers walkable. The same tree is rendered as HTML for a browser.

```
/srv/digfs
  ctl                  search <words> [limit N] [kind a,b] [code] | reload
  stats                encoder, vectors, files, searchable
  roots                the indexed folders
  help
  q/
    newest/            the last query, always
    001/               one directory per query, in order
      query            the line you wrote
      summary          hits, strong count, timings
      paths            one source path per hit
      00-<name>        path, unit, score, then the text of that unit
      01-<name>
```

A hit file looks like this:

```
path: /home/me/notes/pmic.md
kind: doc
unit: chunk 5 (segment 32)
score: 6.1556 (cosine 0.7277, z 4.1556, keyword 1)
file: 10836 bytes, modified 2026-11-01 17:59
---
...the text of that chunk...
```

So `cat` gives you the passage and the pointer in one read, and

```sh
for (p in `{cat /n/dig/q/newest/paths}) echo $p
```

gives the source files to open in place. `q/newest` is not a symlink (9P has
none): it is a directory that always lists the last query's children.

## A first run

```sh
digup download-model                  # once: about 865 MB
digup index ~/notes                   # once: build the index

cd contrib/digfs && go build
./digfs -w 'tcp!*!8080' -root ~/notes
```

Then open `http://localhost:8080/`.  Put `digup` on `PATH`, or name it with
`-digup`.  If your model is somewhere else, pass `-models ~/DigUp/Models`.

The full form:

```sh
./digfs \
  -a 'tcp!*!5650' -w 'tcp!*!8080' \
  -digup /usr/lib/DigUp/helper/digup \
  -index ~/.local/share/digup/index \
  -root ~/notes -root ~/papers \
  -models ~/DigUp/Models
```

`-a` and `-w` take Plan 9 addresses (`net!host!port`, `*` for every
interface).  `-root` is repeatable: it names the folders the engine may
answer for, and the only files the HTML view will serve raw.  **With no
`-root`, every folder in the index answers** and raw links are limited to
mirrored shares.

From Plan 9 or 9front:

```sh
9fs tcp!host!5650          # or: mount -a tcp!host!5650 /n/dig
ls /n/dig/q
```

From Linux:

```sh
sudo mount -t 9p -o trans=tcp,port=5650,version=9p2000,uname=$USER 127.0.0.1 /mnt/dig
```

### The LD_LIBRARY_PATH trap

DigUp ships its own llama.cpp beside `digup` and finds it through `$ORIGIN`.
If `LD_LIBRARY_PATH` names another llama.cpp build, the dynamic linker picks
that one first and the model fails with `unknown model architecture:
'gemma-embedding2'`. Pass `-cleanenv` to run the engine without that variable.

## Mounting other filesystems

The index should not care whether a file is on this disk. digfs speaks 9P as
a client as well as a server, so any 9P tree — another digfs, a 9front
export, a Harvey `/srv` service — can be mounted, browsed and indexed:

```
echo 'mount papers tcp!192.168.2.9!5640 /home/me/papers' >> ctl
ls /mnt/papers                       # live: nothing is copied
echo 'sync papers' >> ctl            # refresh, then index what changed
```

`/mnt/<name>/` is the remote tree itself, walked through digfs. `sync`
refreshes and indexes in one step (`refresh` and `index` are the two halves);
it copies files whose size or modification time changed into
`~/.cache/digfs/fs/<name>/` — the indexer reads local paths, so the mirror is
what makes a remote tree indexable. `.git`, `.hg`, `.svn` and `node_modules`
are skipped, and files over `-maxfetch` (256 MB) are left alone.

A hit from a share says where the file actually lives:

```
path: papers:/2026/foo.pdf
share: papers
cache: /home/me/.cache/digfs/papers/2026/foo.pdf
```

The text of every unit is already in the index, so deleting the cache does
not break search or `text`; only `raw` and "open the file" need the copy.
`refresh <name> [path]` and `index <name> [path]` take a subdirectory, which
is how you index one folder of a large export.

`refresh` reconciles rather than only adds: a file the remote no longer has
is deleted from the mirror, and empty directories go with it, so the next
`index` run drops those units (`sync: … 1 removed`). Subtrees that were not
walked — `.git`, an unreadable directory, anything over `-maxfetch` — are
kept rather than guessed at.

`forget <name>` empties the mirror and indexes the empty directory, which is
how the units of a share leave the index without touching the server.

The indexer's own rules still apply to what it is given: a mounted tree that
looks like a code project (a `.git`, `Package.swift`, a Go module) is skipped
unless you index its directory directly or pass `--include-code` to the
indexer. Refreshes for one share queue rather than fail, so a script can ask
for several subtrees in a row.

`-sync 15m` runs the same thing for every mounted share on a timer, skipping
a share that is already working.

Mounts persist: `<cache>/shares.conf` holds `name \t address \t path` and
digfs re-dials them at startup, so the namespace comes back with the
process. The connections are the only live state; everything else is on
disk. `/shares` reports the mirror's size, removals, state and the last
refresh/index times, and `/log` keeps the last 256 events for an agent that
was not watching stderr.

The engine is started with the cache directory as one of its roots, so
anything refreshed is searchable without restarting.

## The web view

`-w` serves the index as a search page.  That is the whole UI: one box, the
recent questions, a page of cards per query, and the passage behind each hit
(Markdown rendered when the file was Markdown).  It needs nothing but digfs
and the model.

```sh
./digfs -w 'tcp!*!8080' -root ~/notes
```

`http://localhost:8080/` — "Search the files on this machine by what's in
them."  The pages are plain GETs; the only script is the Markdown renderer,
and without it the passage is still there as text.  `/stats` and `/help` are
text files, `/raw/<query>/<hit>` serves the original bytes when the file is
under a `-root`, and `/q/<n>/paths` is the hit list for scripts.

This is the extension, not a replacement for DigUp's own app: index with
`digup index` (or the app) and point digfs at the same index.

## One index across machines

The point of the 9P side is not presentation: it is that another machine's
files can feed the same index, and every hit says where it came from.

```
mount papers tcp!192.168.2.9!5640 /home/me/papers
sync papers
```

`/machines` in the web view does the same with a form: name, address, path.
The host runs any 9P server — another digfs, a 9front export, `exportfs` —
and digfs copies changed files into its cache, runs the indexer over them,
and tags each hit with `papers:/2026/foo.pdf`.  Nothing is written on the
other machine.

The path is walked by the client after attach, not taken from the attach
name: 9P2000 leaves `aname` to the server, and a server that ignores it
would look like success while exposing its whole export (this cost a 2.2 GB
mirror of the wrong tree before it was fixed).

### SMB shares

Office data is usually on SMB, not 9P.  [`../smb9p/`](../smb9p/) is the
adapter: one process holds a service account, serves one share read-only
over 9P, and needs no kernel mount or root.

```
smb9p -server files:445 -share documents -user 'DOMAIN\svc-index' -pass-env SMBSECRET
mount office tcp!smbhost!5640 /documents
sync office
```

Hits then read `office:/lease.md`.  Run one bridge per share and per access
boundary: it carries one account's rights, and the 9P side has no
authentication of its own.

### webbox, if you want a Plan 9 front end

[sirjofri's webbox](https://shithub.us/sirjofri/webbox) is a 9front rc script
that serves a folder over tcp80 CGI.  [`../webbox/`](../webbox/) is a fork
with a **digfs mode**: mount this tree, point webbox at its root, and the
same search page is served by 9front, with snarf/plumb for the text.  It is
an alternative for people already in that world; nothing here depends on it.

```
9fs tcp!indexhost!5650              # or: mount -a tcp!indexhost!5650 /n/dig
webbox 8080 /n/dig                 # needs urldecode(1) and tcp80
```

Verified end to end: a 9front guest mounted this tree, searched through its
`ctl`, and rendered `/f/q/N/` as cards, while the host fetched the pages
through a QEMU forward.

## What it is not

- **Not an indexer.** `digup index` still builds the index; digfs runs it and
  asks the search service questions.
- **Not a model host.** No embeddings, no llama.cpp, no Python: one Go binary
  that speaks JSON to the engine and 9P to clients.
- **No `similar <segment>` yet.** The engine exposes `text` and `search`, not
  "more like this unit". That is one op away in the fork (the vectors are in
  the index, `Searcher` already loads them) and the namespace would not change.
- **No incremental reindex.** Same reason: that belongs to `digup index`, and
  `reload` makes a running digfs see the result.

## Why this shape

Sigil's argument, kept and shrunk: the identifier carries what you need to
find it, results are files, and the walk is the query. DigUp already has the
two expensive parts — readers that turn a PDF page, an audio window or a code
definition into a unit with a location, and an embedder that puts text,
pictures and sound in one space. Those stay where they are; what was missing
is a namespace, and that is this.
