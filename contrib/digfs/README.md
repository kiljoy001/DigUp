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

## What it is not

- **Not an indexer.** `digup index` still builds the index; digfs only asks.
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
