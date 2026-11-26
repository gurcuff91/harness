package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// page is one Markdown file. Front matter (between "---" lines):
//
//	title: Quick start
//	section: Start          (the chapter; chapters are ordered by `order` of their pages)
//	order: 30               (position inside the whole site)
//	summary: One line for search and cards
type page struct {
	title, section, summary string
	order                   int
	slug                    string // "start/quick-start"
	body                    string
}

type site struct {
	pages    []*page
	sections []string // in order of first appearance
}

func load(dir string) (*site, error) {
	s := &site{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		pg, err := parsePage(string(raw))
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		rel, _ := filepath.Rel(dir, p)
		pg.slug = strings.TrimSuffix(filepath.ToSlash(rel), ".md")
		s.pages = append(s.pages, pg)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(s.pages, func(i, j int) bool {
		if s.pages[i].order != s.pages[j].order {
			return s.pages[i].order < s.pages[j].order
		}
		return s.pages[i].slug < s.pages[j].slug
	})
	seen := map[string]bool{}
	for _, p := range s.pages {
		if !seen[p.section] {
			seen[p.section] = true
			s.sections = append(s.sections, p.section)
		}
	}
	return s, nil
}

func parsePage(raw string) (*page, error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(raw, "---\n") {
		return nil, fmt.Errorf("missing front matter")
	}
	end := strings.Index(raw[4:], "\n---")
	if end < 0 {
		return nil, fmt.Errorf("unterminated front matter")
	}
	p := &page{body: strings.TrimLeft(raw[4+end+4:], "\n")}
	for _, l := range strings.Split(raw[4:4+end], "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "title":
			p.title = v
		case "section":
			p.section = v
		case "summary":
			p.summary = v
		case "order":
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("order %q is not a number", v)
			}
			p.order = n
		}
	}
	if p.title == "" || p.section == "" {
		return nil, fmt.Errorf("title and section are required")
	}
	return p, nil
}
