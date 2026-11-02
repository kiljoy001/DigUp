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

## Running it

```sh
cd contrib/digfs
go build

./digfs \
  -a 'tcp!*!5650' -w 'tcp!*!8080' \
  -digup /usr/lib/DigUp/helper/digup \
  -index ~/.local/share/digup/index \
  -root ~/notes -root ~/papers \
  -models ~/DigUp/Models
```

`-a` and `-w` take Plan 9 addresses (`net!host!port`, `*` for every
interface). `-root` is repeatable and only guards the HTML view's file links:
9P clients resolve `path:` themselves.

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

## Presenting it: webbox

[sirjofri's webbox](https://shithub.us/sirjofri/webbox) is a 9front rc
script: give it a port and a folder, and it serves that folder as HTML over
tcp80 CGI, with a raw link for every file and snarf/plumb for the text. The
query directories are ordinary directories, so the presentation is a mount
and a command:

```
9fs tcp!indexhost!5650              # or: mount -a tcp!indexhost!5650 /n/dig
webbox 8080 /n/dig/q/newest         # needs urldecode(1) and tcp80
```

`/f/q/newest/00-foo.md` shows the passage, `/files/...` the raw file. The
built-in `-w` view remains the zero-dependency fallback.

The source is at <https://shithub.us/sirjofri/webbox/HEAD/info.html> — note
the `/HEAD/info.html`, the bare `/sirjofri` path is not served. It needs
`urldecode(1)` and [tcp80](https://shithub.us/grobe0ba/tcp80/HEAD/info.html);
both build with `mk install` on a 9front box.

Verified end to end here: a 9front guest mounted this tree, ran webbox over
`q/newest`, and the host fetched the rendered pages through a QEMU port
forward.

```
# 9front
9fs tcp!10.0.2.2!5650
echo 'search search results as files over 9P' > '/n/10.0.2.2!5650/ctl'
webbox 8080 '/n/10.0.2.2!5650/q/newest'

# host (QEMU user networking; add the forward over QMP)
curl -k https://127.0.0.1:18080/f/
curl -k https://127.0.0.1:18080/files/00-DESIGN.md
```

The listing comes back as `00-DESIGN.md … 05-pmic-bringup.md` with `/f/` and
`/files/` links, and the raw link starts with `path: repo:/sigil/docs/…`.

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
