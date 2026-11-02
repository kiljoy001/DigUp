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

## Licence

Upstream carries no licence file.  This copy is kept here as a fork with
the author credited; check with sirjofri before redistributing it as a
package.
