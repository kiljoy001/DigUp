/*

     mkdparse (mkd.js) - a wannabe markdown parser

     Original author: sirjofri
     Version: 0.1
     https://github.com/sirjofri/mkdparse

     Modified for webbox (2026): a line-based pass instead of regex over the
     whole document, so fenced code, ordered lists, `###`, `---`, inline
     code and links work and code is never re-parsed as markup.  The input
     is a <pre> whose innerHTML is already HTML-escaped, which is what keeps
     a file containing <script> a document rather than a script.

You are free to use this software for whatever you want, but please look at the following points.

- You are **allowed** to send me a gift if you like.
- You are **allowed** to send the whole package to someone else, unchanged and as it is.
- You are **allowed** to extract the `mkd.js` file, as long as you provide this license information somewhere.
- You are **allowed** to use portions of the code for whatever you like.
- You are **not allowed** to receive money for this software package alone. You need to at least change something.
- In case you change something: Leave the license information in the header of the file and change the "Author" to "Original Author".
- If you do that with some bigger portions (not one- or two-liners) please leave a note with a link (a comment in the code is enough)
- The software is not heavily tested. Please do not use it under weird circumstances (like: hanging on the international space station searching for the key you lost)
- Don't complain if this software kills your cat, dog or cow!
- You are allowed to contribute (if you really want. I mean, this software is more ore less abandoned...).
*/

var mkd = function(input, output) {
	this.input = input;
	this.output = output;
};

mkd.prototype.draw = function() {
	this.output.innerHTML = this.parse(this.input.innerHTML);
};

mkd.prototype.escape = function(s) {
	return s;
};

mkd.prototype.inline = function(s) {
	// Input is already escaped HTML; these only add tags.
	s = s.replace(/`([^`]+)`/g, '<code>$1</code>');
	s = s.replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, '<a href="$2" rel="noreferrer">$1</a>');
	s = s.replace(/\*\*([^*]+)\*\*/g, '<b>$1</b>');
	s = s.replace(/__([^_]+)__/g, '<b>$1</b>');
	s = s.replace(/(^|[^*])\*([^*\n]+)\*/g, '$1<i>$2</i>');
	s = s.replace(/(^|[^\w])_([^_\n]+)_/g, '$1<i>$2</i>');
	return s;
};

// Collect the items of one list, starting at line i.  A hard-wrapped item
// continues on the next line; a blank line between items is allowed (a
// "loose" list) as long as another item follows.  Anything else ends it.
mkd.prototype.list = function(lines, i, marker) {
	var items = [];
	var cur = null;
	while (i < lines.length) {
		var line = lines[i];
		if (/^\s*$/.test(line)) {
			var j = i + 1;
			while (j < lines.length && /^\s*$/.test(lines[j])) j++;
			if (j < lines.length && marker.test(lines[j])) { i = j; continue; }
			break;
		}
		var m = line.match(marker);
		if (m) {
			if (cur !== null) items.push(cur);
			cur = m[1];
			i++;
			continue;
		}
		if (/^(#{1,6})\s/.test(line) || /^```/.test(line) || /^\s*>/.test(line) ||
		    /^\s*(---+|\*\*\*+|___+)\s*$/.test(line))
			break;
		if (cur !== null) {
			cur += ' ' + line.replace(/^\s+/, '');
			i++;
			continue;
		}
		break;
	}
	if (cur !== null) items.push(cur);
	return { items: items, next: i };
};

mkd.prototype.parse = function(text) {
	text = text.replace(/\r\n/g, '\n').replace(/\t/g, '    ');
	var lines = text.split('\n');
	var out = [];
	var para = [];
	var self = this;

	function flush() {
		if (para.length) {
			out.push('<p>' + self.inline(para.join('\n')) + '</p>');
			para = [];
		}
	}

	var i = 0;
	while (i < lines.length) {
		var line = lines[i];

		var fence = line.match(/^```(.*)$/);
		if (fence) {
			flush();
			i++;
			var buf = [];
			while (i < lines.length && !/^```\s*$/.test(lines[i]))
				buf.push(lines[i++]);
			if (i < lines.length) i++;
			out.push('<pre class="code"><code>' + buf.join('\n') + '</code></pre>');
			continue;
		}

		if (/^\s*$/.test(line)) { flush(); i++; continue; }

		var h = line.match(/^(#{1,6})\s+(.*?)\s*#*\s*$/);
		if (h) {
			flush();
			var n = h[1].length;
			out.push('<h' + n + '>' + self.inline(h[2]) + '</h' + n + '>');
			i++;
			continue;
		}

		if (/^\s*(---+|\*\*\*+|___+)\s*$/.test(line)) {
			flush(); out.push('<hr>'); i++; continue;
		}

		if (/^\s*>\s?/.test(line)) {
			flush();
			var q = [];
			while (i < lines.length && /^\s*>\s?/.test(lines[i]))
				q.push(lines[i++].replace(/^\s*>\s?/, ''));
			out.push('<blockquote>' + self.parse(q.join('\n')) + '</blockquote>');
			continue;
		}

		var ul = line.match(/^\s*[-*+]\s+(.*)$/);
		if (ul) {
			flush();
			var items = self.list(lines, i, /^\s*[-*+]\s+(.*)$/);
			i = items.next;
			out.push('<ul>' + items.items.map(function(x) { return '<li>' + self.inline(x) + '</li>'; }).join('') + '</ul>');
			continue;
		}

		var ol = line.match(/^\s*\d+[.)]\s+(.*)$/);
		if (ol) {
			flush();
			var items2 = self.list(lines, i, /^\s*\d+[.)]\s+(.*)$/);
			i = items2.next;
			out.push('<ol>' + items2.items.map(function(x) { return '<li>' + self.inline(x) + '</li>'; }).join('') + '</ol>');
			continue;
		}

		para.push(line);
		i++;
	}
	flush();
	return out.join('\n');
};
