package main

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type searchEntry struct {
	Title   string `json:"t"`
	Section string `json:"s"`
	URL     string `json:"u"`
	Text    string `json:"x"`
}

func render(s *site, staticDir, out, version, base string) error {
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	if err := copyDir(staticDir, out); err != nil {
		return err
	}
	known := map[string]bool{}
	for _, p := range s.pages {
		known[p.slug] = true
	}
	var index []searchEntry
	for i, p := range s.pages {
		// A link written as "../frontends/slack.md" or "slack.md" is resolved
		// against the page's own directory, and must exist.
		var bad []string
		link := func(href string) string {
			if strings.Contains(href, "://") || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "mailto:") {
				return href
			}
			h, frag, _ := strings.Cut(href, "#")
			if !strings.HasSuffix(h, ".md") {
				return href
			}
			target := path.Clean(path.Join(path.Dir(p.slug), strings.TrimSuffix(h, ".md")))
			if !known[target] {
				bad = append(bad, href)
				return "#"
			}
			u := base + "/" + target + ".html"
			if frag != "" {
				u += "#" + frag
			}
			return u
		}
		body, heads := renderMarkdown(p.body, link)
		if len(bad) > 0 {
			return fmt.Errorf("%s: broken link(s): %s", p.slug, strings.Join(bad, ", "))
		}
		var prev, next *page
		if i > 0 {
			prev = s.pages[i-1]
		}
		if i+1 < len(s.pages) {
			next = s.pages[i+1]
		}
		doc := pageHTML(s, p, body, heads, prev, next, version, base)
		dst := filepath.Join(out, filepath.FromSlash(p.slug)+".html")
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, []byte(doc), 0o644); err != nil {
			return err
		}
		index = append(index, searchEntry{p.title, p.section, base + "/" + p.slug + ".html", plain(p.body)})
	}
	if len(s.pages) > 0 { // the root is the first page
		first := base + "/" + s.pages[0].slug + ".html"
		root := `<!doctype html><meta charset="utf-8"><meta http-equiv="refresh" content="0; url=` + first + `"><a href="` + first + `">Documentation</a>`
		if err := os.WriteFile(filepath.Join(out, "index.html"), []byte(root), 0o644); err != nil {
			return err
		}
	}
	js, _ := json.Marshal(index)
	if err := os.WriteFile(filepath.Join(out, "search.json"), js, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, ".nojekyll"), nil, 0o644)
}

// plain strips Markdown down to searchable text.
func plain(md string) string {
	var b strings.Builder
	for _, l := range strings.Split(md, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "```") || strings.HasPrefix(l, "|--") || strings.HasPrefix(l, "| --") {
			continue
		}
		l = strings.NewReplacer("`", "", "**", "", "#", "", ">", "", "|", " ").Replace(l)
		if l != "" {
			b.WriteString(l)
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func pageHTML(s *site, p *page, body string, heads []heading, prev, next *page, version, base string) string {
	var nav strings.Builder
	for _, sec := range s.sections {
		fmt.Fprintf(&nav, "<h4>%s</h4>\n", html.EscapeString(sec))
		for _, q := range s.pages {
			if q.section != sec {
				continue
			}
			cls := ""
			if q == p {
				cls = ` class="on"`
			}
			fmt.Fprintf(&nav, `<a%s href="%s/%s.html">%s</a>`+"\n", cls, base, q.slug, html.EscapeString(q.title))
		}
	}
	var toc strings.Builder
	if len(heads) > 0 {
		toc.WriteString("<h5>On this page</h5>\n")
		for _, h := range heads {
			cls := "l2"
			if h.Level == 3 {
				cls = "l3"
			}
			fmt.Fprintf(&toc, `<a class="%s" href="#%s">%s</a>`+"\n", cls, h.ID, html.EscapeString(strings.ReplaceAll(h.Text, "`", "")))
		}
	}
	var pager strings.Builder
	pager.WriteString(`<div class="pager">`)
	if prev != nil {
		fmt.Fprintf(&pager, `<a href="%s/%s.html">← <b>%s</b><span>%s</span></a>`, base, prev.slug, html.EscapeString(prev.title), html.EscapeString(prev.section))
	} else {
		pager.WriteString(`<span></span>`)
	}
	if next != nil {
		fmt.Fprintf(&pager, `<a class="next" href="%s/%s.html"><b>%s</b> →<span>%s</span></a>`, base, next.slug, html.EscapeString(next.title), html.EscapeString(next.section))
	}
	pager.WriteString(`</div>`)

	r := strings.NewReplacer(
		"{{title}}", html.EscapeString(p.title),
		"{{summary}}", html.EscapeString(p.summary),
		"{{section}}", html.EscapeString(p.section),
		"{{version}}", html.EscapeString(version),
		"{{base}}", base,
		"{{nav}}", nav.String(),
		"{{toc}}", toc.String(),
		"{{pager}}", pager.String(),
		"{{body}}", body,
		"{{slug}}", html.EscapeString(p.slug),
	)
	return r.Replace(pageTemplate)
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(t, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(t, b, 0o644)
	})
}

const pageTemplate = `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{title}} · harness</title><meta name="description" content="{{summary}}">
<link rel="stylesheet" href="{{base}}/style.css">
</head><body data-base="{{base}}">
<header class="top">
  <button class="menu" aria-label="Menu">☰</button>
  <a class="logo" href="{{base}}/">harness<b>_</b></a>
  <div class="sp"></div>
  <div class="search"><input id="q" type="search" placeholder="⌕ search docs…  /" autocomplete="off"><div id="results" hidden></div></div>
  <span class="ver">{{version}}</span>
  <a class="gh" href="https://github.com/gurcuff91/harness">GitHub ↗</a>
</header>
<div class="wrap">
<nav id="nav">{{nav}}</nav>
<main>
<div class="crumb">{{section}} <span>/</span> {{title}}</div>
<h1>{{title}}</h1>
{{body}}
{{pager}}
</main>
<aside>{{toc}}</aside>
</div>
<footer class="status"><span><em>●</em> idle</span><span>docs / {{slug}}</span><span class="sp"></span><span>/ search · ← → pages</span></footer>
<script src="{{base}}/app.js"></script>
</body></html>
`
