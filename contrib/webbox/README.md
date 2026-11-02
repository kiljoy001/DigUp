# webbox, restyled

A fork of [sirjofri's webbox](https://shithub.us/sirjofri/webbox/HEAD/info.html):
an rc script that serves a directory over `tcp80` (CGI) and `tlssrv`.

Upstream is readable but plain — a grey header, every link a lavender pill,
a bulleted listing, and a file rendered as one undifferentiated `<pre>`.
This fork keeps the same URLs (`/`, `/f/…`, `/files/…`, `/snarf`,
`/dosnarf.html`, `/doplumb.html`) and changes only the presentation:

- light and dark palettes through `prefers-color-scheme`, one green accent,
  a sticky header bar and a footer that stays at the bottom on short pages;
- breadcrumbs built from the path, a title, and an item count;
- the listing as rows — name on the left, a `raw` pill on the right, hover
  state, and a small arrow for directories (no glyph font needed);
- **a metadata card for digfs results**: a file that begins with `path:`
  and has a `---` line is rendered as `share:/path` plus chips for unit,
  score, kind and size, with the passage below it. It is the one
  content-aware piece; everything else is a generic file browser;
- the home page as cards, with the optional clipboard snarf;
- names are HTML-escaped (upstream interpolated them raw), so a file called
  `<b>` is a file, not markup.

## digfs mode

If the folder it is pointed at is a digfs tree — it has `ctl` and `q/` —
webbox becomes a search interface instead of a file browser:

- `/` is a search box with the recent searches underneath, and a link to
  browse everything;
- `/search?q=…` writes `search …` (or `code …`) to `ctl` and renders the
  newest query;
- `/f/q/N/` renders that query as cards: where the text came from
  (`share:/path`), the unit, the score, a three-line preview, and a link to
  the passage;
- `/f/q/N/NN-file` is the passage, with the metadata card and a *back to
  results* button;
- `/f/…` still browses everything, including mounted shares under `/mnt`.

Every URL is a plain GET; no JavaScript, no fetch, no session.  Point it at
the root of the tree:

```
webbox 8080 /n/dig
```

## Installing

This directory has only the script.  Take `urldecode.c` and the `mkfile`
from upstream (unchanged), then:

```
mk install
webbox 8080 /n/dig/q/newest
```

It needs `tcp80` (<https://shithub.us/grobe0ba/tcp80/HEAD/info.html>) and
`urldecode` on a 9front box.

## The blank line

`tcp80` writes the status line and headers itself for a rule and then execs
the CGI with the connection as stdout — but it never writes the blank line
that ends the header block.  The CGI's first byte has to be `\n`, or the
response is a malformed header block: the browser sees `200 OK` and then
hangs up, and every write in the script fails with `i/o on hungup channel`.
Upstream's header string starts with a newline for exactly this reason;
`header.html` here begins with a blank line for the same reason.  It is
worth knowing before rewriting the page assembly.

## Notes for whoever edits this

Ninefront rc is not sh, and tcp80 does not shield you from that.  The five
things that cost the most time:

- **The blank line.** `tcp80` writes the status line and headers for a rule
  and then execs the CGI with the connection as stdout, but never writes the
  blank line that ends the header block.  The CGI's first byte must be `\n`;
  `header.html` starts with one.  Without it the browser sees `200 OK` and
  the connection dies, and every write fails with `i/o on hungup channel`.
- `x=` is a syntax error (empty assignment).  Use `x=''` or `x=()`.
- `awk -v q=$pat` parses `q=$pat` as an assignment and fails.  Quote the
  prefix: `awk -v 'q='$pat`.
- `ls dir` prints `dir/name`, not `name`.  Strip it with `sed 's|.*/||'`
  before matching on the basename (this silently produced empty listings).
- `'search '^$list` prefixes *every* element of the list; `echo search $list`
  joins the elements with spaces, which is what a command line wants.
- rc has `break` but no `continue`; filter the list before the loop.

## Licence

Upstream carries no licence file.  This copy is kept here as a fork with
the author credited; check with sirjofri before redistributing it as a
package.
