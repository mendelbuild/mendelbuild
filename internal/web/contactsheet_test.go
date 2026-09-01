package web

import (
	"encoding/base64"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The contact sheet: every page state the suite builds, on one scrollable page.
//
//	MENDEL_CONTACT_SHEET=/tmp/mendel-ui.html go test -count=1 ./internal/web/
//
// -count=1 matters: a cached test run does not execute TestMain, so the sheet
// is silently not rebuilt and you review the previous one.
//
// The file is self-contained — stylesheets inlined, no server needed — so it
// opens straight from disk in a browser. Every state carries a stable number,
// which is the point: feedback can say "#12: the badge is misaligned" instead
// of describing which screen it means.
//
// It is assembled in TestMain, after the tests have run, because the states
// come from the fixtures those tests already construct. Rendering a second set
// here would mean a review surface that drifts from what the suite covers.

func TestMain(m *testing.M) {
	out := os.Getenv("MENDEL_CONTACT_SHEET")
	if out == "" {
		os.Exit(m.Run())
	}

	// The sheet is built from the dumps, so turn those on for this run unless
	// the caller has already chosen somewhere to put them.
	dumpDir := os.Getenv("MENDEL_PAGE_DUMP_DIR")
	temporary := dumpDir == ""
	if temporary {
		dir, err := os.MkdirTemp("", "mendel-pages")
		if err != nil {
			fmt.Fprintf(os.Stderr, "contact sheet: %v\n", err)
			os.Exit(1)
		}
		dumpDir = dir
		os.Setenv("MENDEL_PAGE_DUMP_DIR", dumpDir)
	}

	code := m.Run()

	if err := buildContactSheet(dumpDir, out); err != nil {
		fmt.Fprintf(os.Stderr, "contact sheet: %v\n", err)
		if code == 0 {
			code = 1
		}
	} else {
		fmt.Fprintf(os.Stderr, "contact sheet written to %s\n", out)
	}
	if temporary {
		os.RemoveAll(dumpDir)
	}
	os.Exit(code)
}

var (
	bodyRE = regexp.MustCompile(`(?s)<body>(.*)</body>`)

	// A dumped page carries the behaviour of the real thing, and on one sheet
	// that behaviour is destructive: the deployment page refreshes itself every
	// three seconds while a validation runs, the decision queue and the setup
	// screen each reload after a timeout, and the log tailer reloads when a
	// status changes. Any one of them reloads the whole contact sheet out from
	// under whoever is reading it.
	//
	// The sheet is for looking at layout, copy and state. None of that needs a
	// script, and the two things scripts would have drawn -- the roadmap graph
	// and streamed log lines -- are already absent or server-rendered. So they
	// come out.
	scriptRE = regexp.MustCompile(`(?s)<script\b[^>]*>.*?</script>`)
	metaRE   = regexp.MustCompile(`(?i)<meta[^>]*http-equiv[^>]*>`)

	// A bar's length is data, so the app carries it in a data attribute and a
	// script applies it -- which keeps `style=` out of the templates, where the
	// lint forbids it. With the scripts gone, the sheet has to do that job
	// itself, or every meter renders full-width and every budget looks spent.
	//
	// Writing an inline style is right here and wrong there: this file is a
	// generated artifact, not a template, and it has no second render in which
	// to drift.
	widthRE = regexp.MustCompile(`data-(?:meter-fill|tl-width|w)="([0-9.]+)"`)
	leftRE  = regexp.MustCompile(`data-(?:meter-mark|tl-at|x)="([0-9.]+)"`)
	// The timeline's "today" line is positioned against the label column, so
	// its offset is an expression rather than a plain percentage.
	todayRE = regexp.MustCompile(`data-tl-today="([0-9.]+)"`)
	// An element carrying both a position and a width has to become one style
	// attribute. Emitting two leaves the second ignored, since HTML keeps the
	// first of a repeated attribute — which drew every timeline bar at zero
	// width while looking, in the markup, entirely correct.
	pairRE = regexp.MustCompile(`data-tl-at="([0-9.]+)"\s+data-tl-width="([0-9.]+)"`)
	titleRE = regexp.MustCompile(`(?s)<title>(.*?)</title>`)
	// Dump filenames are "<TestName>--<template>.html".
	nameRE = regexp.MustCompile(`^(.*)--([^-]+)\.html$`)
)

type specimenPage struct {
	Num      int
	Test     string
	Template string
	Body     string
}

func buildContactSheet(dumpDir, out string) error {
	entries, err := os.ReadDir(dumpDir)
	if err != nil {
		return err
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".html") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return fmt.Errorf("no page dumps in %s", dumpDir)
	}
	// Group by template, so related states sit together and the numbering is
	// stable between runs.
	sort.Slice(names, func(i, j int) bool {
		ti, tj := templateOf(names[i]), templateOf(names[j])
		if ti != tj {
			return ti < tj
		}
		return names[i] < names[j]
	})

	// The logo is the one asset the pages reference that is worth keeping: 37
	// copies of broken-image alt text is a distraction from the thing being
	// reviewed. Everything else the pages pull (the log tailer, the graph
	// renderer) is behaviour, and a static review does not need it.
	logo, err := os.ReadFile(filepath.Join("static", "mendel-logo-transparent-32.png"))
	if err != nil {
		return err
	}
	logoURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(logo)

	pages := make([]specimenPage, 0, len(names))
	for i, name := range names {
		raw, err := os.ReadFile(filepath.Join(dumpDir, name))
		if err != nil {
			return err
		}
		body := ""
		if m := bodyRE.FindSubmatch(raw); m != nil {
			body = string(m[1])
		}
		body = scriptRE.ReplaceAllString(body, "")
		body = metaRE.ReplaceAllString(body, "")
		body = pairRE.ReplaceAllString(body, `style="left:$1%;width:$2%"`)
		body = widthRE.ReplaceAllString(body, `style="width:$1%"`)
		body = leftRE.ReplaceAllString(body, `style="left:$1%"`)
		body = todayRE.ReplaceAllStringFunc(body, func(m string) string {
			pctStr := todayRE.FindStringSubmatch(m)[1]
			f, err := strconv.ParseFloat(pctStr, 64)
			if err != nil {
				return m
			}
			return fmt.Sprintf(`style="left:calc(var(--tl-label) + (100%% - var(--tl-label)) * %.4f)"`, f/100)
		})
		body = strings.ReplaceAll(body, `src="/static/mendel-logo-transparent-32.png"`,
			`src="`+logoURI+`"`)
		body = scriptRE.ReplaceAllString(body, "")
		body = metaRE.ReplaceAllString(body, "")
		body = pairRE.ReplaceAllString(body, `style="left:$1%;width:$2%"`)
		body = widthRE.ReplaceAllString(body, `style="width:$1%"`)
		body = leftRE.ReplaceAllString(body, `style="left:$1%"`)
		body = todayRE.ReplaceAllStringFunc(body, func(m string) string {
			pctStr := todayRE.FindStringSubmatch(m)[1]
			f, err := strconv.ParseFloat(pctStr, 64)
			if err != nil {
				return m
			}
			return fmt.Sprintf(`style="left:calc(var(--tl-label) + (100%% - var(--tl-label)) * %.4f)"`, f/100)
		})
		body = strings.ReplaceAll(body, `src="/static/mendel-logo-transparent-32.png"`,
			`src="`+logoURI+`"`)
		pages = append(pages, specimenPage{
			Num:      i + 1,
			Test:     humanise(testOf(name)),
			Template: templateOf(name),
			Body:     body,
		})
	}

	var css strings.Builder
	for _, sheet := range []string{"tokens.css", "components.css"} {
		b, err := os.ReadFile(filepath.Join("static", "css", sheet))
		if err != nil {
			return err
		}
		css.Write(b)
		css.WriteString("\n")
	}

	var page strings.Builder
	page.WriteString(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Mendel UI contact sheet</title>
<style>
`)
	page.WriteString(css.String())
	page.WriteString(`
/* Contact-sheet chrome. Prefixed so nothing here can be mistaken for, or
   collide with, the app's own components. */
/* The index sits at the top and scrolls away. Pinned, forty entries took most
   of the window and left a letterbox to review through. The specimen label is
   the part worth pinning: it says which screen you are looking at, which is the
   thing you lose when you scroll. */
.cs-head { background: var(--surface-card); border-bottom: 1px solid var(--line-strong);
  padding: var(--sp-3) var(--sp-5); }
.cs-index { display: flex; flex-wrap: wrap; gap: 2px var(--sp-3);
  max-width: var(--page-max); margin: 0 auto; }
.cs-index a { font-size: var(--text-xs); color: var(--ink-2); text-decoration: none;
  white-space: nowrap; }
.cs-index a:hover { color: var(--tone-progress); text-decoration: underline; }
.cs-specimen { border-top: 8px solid var(--surface-page); }
.cs-label { position: sticky; top: 0; z-index: 40; display: flex; gap: var(--sp-3);
  align-items: baseline; background: var(--ink-1); color: var(--ink-inverse);
  padding: var(--sp-2) var(--sp-5); font-size: var(--text-sm); }
.cs-num { font-weight: 700; font-variant-numeric: tabular-nums; }
.cs-tpl { margin-left: auto; opacity: 0.6; font-family: var(--font-mono); font-size: var(--text-xs); }
.cs-frame { background: var(--surface-page); }
/* Page dumps carry their own nav; it is worth seeing, but not at full height
   35 times over. */
.cs-frame .nav { height: 40px; }
</style></head><body>
<header class="cs-head"><nav class="cs-index">
`)
	for _, p := range pages {
		fmt.Fprintf(&page, `<a href="#s%d">%d %s</a>`+"\n", p.Num, p.Num, html.EscapeString(p.Test))
	}
	page.WriteString("</nav></header>\n")

	for _, p := range pages {
		fmt.Fprintf(&page, `<section class="cs-specimen" id="s%d">
<div class="cs-label"><span class="cs-num">%d</span><span>%s</span><span class="cs-tpl">%s</span></div>
<div class="cs-frame">%s</div>
</section>
`, p.Num, p.Num, html.EscapeString(p.Test), html.EscapeString(p.Template), p.Body)
	}

	page.WriteString("</body></html>\n")
	return os.WriteFile(out, []byte(page.String()), 0o644)
}

func testOf(name string) string {
	if m := nameRE.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return name
}

func templateOf(name string) string {
	if m := nameRE.FindStringSubmatch(name); m != nil {
		return m[2]
	}
	return name
}

// humanise turns "TestOverviewSortsWorkByWhoseMoveItIs_ready_to_choose" into
// something readable, since the test name is the only description each state has.
func humanise(name string) string {
	name = strings.TrimPrefix(name, "Test")
	name = strings.ReplaceAll(name, "_", " ")
	// Split the camel case of the test function name.
	var b strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' && name[i-1] != ' ' {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
