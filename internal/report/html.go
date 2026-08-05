package report

import (
	"fmt"
	"html/template"
	"io"
)

// HTML renders a report as a self-contained HTML page: no external stylesheet,
// no script, nothing that needs a second file alongside it. A report that only
// works if it's opened next to a CSS file is a report nobody actually opens six
// months later.
//
// html/template, not text/template: every value here — target names,
// repository paths, and above all assertion messages — can contain characters
// that came out of a customer's backup. A message is free text built from
// whatever filenames or query results happened to be in the target; treating it
// as trusted HTML would make the tool that reports on your backup's safety a
// stored-XSS vector for it.
func HTML(w io.Writer, r Report) error {
	return htmlTemplate.Execute(w, r)
}

var htmlTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"upper": func(v Verdict) string {
		switch v {
		case Pass:
			return "PASS"
		case Fail:
			return "FAIL"
		default:
			return "ERROR"
		}
	},
	"class": func(v Verdict) string {
		switch v {
		case Pass:
			return "pass"
		case Fail:
			return "fail"
		default:
			return "error"
		}
	},
	"ms": func(ms int64) string {
		if ms <= 0 {
			return "—"
		}
		if ms < 1000 {
			return fmt.Sprintf("%dms", ms)
		}
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	},
}).Parse(htmlSource))

// Kept in one file rather than embedded from a .html asset: this is v0's only
// template, and a separate file plus go:embed is a layer of indirection with
// nothing on the other side of it yet.
const htmlSource = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>constat report — {{.Host}}</title>
<style>
  :root { color-scheme: light dark; }
  body { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
         max-width: 860px; margin: 2rem auto; padding: 0 1rem; line-height: 1.5; }
  h1 { font-size: 1.1rem; margin-bottom: 0; }
  .meta { color: #767676; font-size: 0.9rem; margin-top: 0.25rem; }
  .verdict { display: inline-block; padding: 0.15em 0.6em; border-radius: 0.3em;
             font-weight: 700; letter-spacing: 0.03em; }
  .verdict.pass { background: #1a7f37; color: #fff; }
  .verdict.fail { background: #cf222e; color: #fff; }
  .verdict.error { background: #9a6700; color: #fff; }
  table { border-collapse: collapse; width: 100%; margin: 0.75rem 0 2rem; }
  th, td { text-align: left; padding: 0.35em 0.6em; border-bottom: 1px solid #d0d7de33; }
  th { font-weight: 600; font-size: 0.85rem; color: #767676; }
  td.msg { word-break: break-word; }
  .target { margin-top: 2rem; }
  .target h2 { font-size: 1rem; margin-bottom: 0.1rem; }
  .target .meta { margin-bottom: 0.5rem; }
  footer { color: #767676; font-size: 0.8rem; margin-top: 3rem; }
</style>
</head>
<body>

<h1>constat report <span class="verdict {{class .Verdict}}">{{upper .Verdict}}</span></h1>
<div class="meta">{{.GeneratedAt.Format "2006-01-02 15:04:05 UTC"}} · {{.Host}} · constat {{.Version}} · schema v{{.SchemaVersion}}</div>

{{range .Targets}}
<section class="target">
  <h2>{{.Name}} <span class="verdict {{class .Verdict}}">{{upper .Verdict}}</span></h2>
  <div class="meta">{{.Kind}} · {{.Repository}}{{if .RestoreDurationMs}} · restored in {{ms .RestoreDurationMs}}{{end}}</div>
  <table>
    <thead><tr><th>assertion</th><th>verdict</th><th>message</th><th>duration</th></tr></thead>
    <tbody>
    {{range .Assertions}}
      <tr>
        <td>{{.Name}}</td>
        <td><span class="verdict {{class .Verdict}}">{{upper .Verdict}}</span></td>
        <td class="msg">{{.Message}}</td>
        <td>{{ms .DurationMs}}</td>
      </tr>
    {{else}}
      <tr><td colspan="4"><em>no assertions ran</em></td></tr>
    {{end}}
    </tbody>
  </table>
</section>
{{else}}
<p><em>no targets in this run</em></p>
{{end}}

<footer>constat — <a href="https://github.com/mxmchrbrt/constat">github.com/mxmchrbrt/constat</a></footer>
</body>
</html>
`
