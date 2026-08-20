package preview

import (
	"fmt"
	"html/template"
	"net/http"
	"time"
)

// Overlay returns the HTML fragment injected before </body> of proxied
// preview pages. It hosts the badge panel in a sandboxed iframe served from
// the broker-owned reserved path, with a postMessage protocol that the later
// interactive-edit chat can reuse.
func Overlay(runID string) string {
	return fmt.Sprintf(`<iframe id="harness-preview-panel" title="Agent Harness preview" src="/previews/%s/__harness__/panel" sandbox="allow-scripts allow-popups allow-popups-to-escape-sandbox" style="position:fixed;right:16px;bottom:16px;width:min(400px,calc(100vw - 32px));height:56px;border:0;background:transparent;z-index:2147483647;color-scheme:light"></iframe>
<script>(function(){var f=document.getElementById('harness-preview-panel');window.addEventListener('message',function(e){if(e.source!==f.contentWindow||!e.data||e.data.scope!=='agent-harness.preview')return;if(e.data.type==='resize'){f.style.height=Math.min(Number(e.data.height)||56,Math.max(56,window.innerHeight-32))+'px';}});})();</script>`, template.HTMLEscapeString(runID))
}

// PanelData describes the run behind a preview for the overlay panel.
type PanelData struct {
	RunID          string
	IssueKey       string
	PullRequestURL string
	ExpiresAt      *time.Time
}

// PanelHandler serves the overlay panel document from the broker's reserved
// path. The lookup callback resolves the run behind the authorized preview.
func PanelHandler(lookup func(runID string) (PanelData, bool)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, ok := lookup(r.PathValue("run_id"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = panelTemplate.Execute(w, data)
	})
}

var panelTemplate = template.Must(template.New("panel").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><style>
body{margin:0;font:13px/1.4 system-ui,sans-serif;color:#e7e7ef}
.badge{display:flex;align-items:center;gap:8px;background:#15161d;border:1px solid #2c2e3a;border-radius:12px;padding:10px 14px;cursor:pointer;box-shadow:0 6px 24px rgba(0,0,0,.35)}
.dot{width:8px;height:8px;border-radius:50%;background:#7c6cff;flex:none}
.badge b{font-weight:600}
.panel{display:none;background:#15161d;border:1px solid #2c2e3a;border-radius:12px;padding:14px;margin-top:8px}
.panel.open{display:block}
.panel a{color:#9f92ff}
.row{margin:4px 0;color:#a7a9b8}
.row b{color:#e7e7ef;font-weight:600}
.soon{margin-top:10px;padding:8px 10px;border:1px dashed #3a3d4d;border-radius:8px;color:#8a8da0}
</style></head><body>
<div class="badge" id="badge"><span class="dot"></span><span>Agent Harness <b>preview</b>{{if .IssueKey}} &middot; {{.IssueKey}}{{end}}</span><span id="chev" style="margin-left:auto">&#9650;</span></div>
<div class="panel" id="panel">
<div class="row">Run <b>{{.RunID}}</b></div>
{{if .PullRequestURL}}<div class="row"><a href="{{.PullRequestURL}}" target="_blank" rel="noopener">Open draft pull request</a></div>{{end}}
{{if .ExpiresAt}}<div class="row">Preview expires <b>{{.ExpiresAt.UTC.Format "15:04 UTC"}}</b> (extends while in use)</div>{{end}}
<div class="soon">Interactive edits with Codex will appear here soon.</div>
</div>
<script>(function(){var open=false,badge=document.getElementById('badge'),panel=document.getElementById('panel'),chev=document.getElementById('chev');
function post(){parent.postMessage({scope:'agent-harness.preview',type:'resize',height:document.body.scrollHeight+4},'*');}
badge.addEventListener('click',function(){open=!open;panel.classList.toggle('open',open);chev.innerHTML=open?'&#9660;':'&#9650;';post();});
post();})();</script>
</body></html>`))
