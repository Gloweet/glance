package glance

// Three small server-side routes for the "Récap IA — Veille tech" dashboard
// widget (config in Gloweet/gitops-demo, apps/base/glance/config.yaml).
// Glance's own client-side JS replaces widget content via `innerHTML` even
// on first page load (see static/js/page.js, fetchPageContent), which never
// executes embedded <script> tags — so real interactivity (a click that
// persists state, a search box) has to live here, in the backend, rather
// than in a custom-api template. Plain links and forms still work fine
// (normal browser navigation), which is what these routes are for.
//
// All three ride on whatever auth already protects this Glance instance
// (e.g. an Authentik forward-auth Traefik middleware in front of the whole
// host) — no auth code here.
//
// Digest storage: PheonBest/blogs, digests/ (public repo, read
// unauthenticated via raw.githubusercontent.com). Writing (the cadence
// control) needs a token with Contents:write on that repo, from
// VEILLE_GITHUB_TOKEN.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	veilleDefaultRepo       = "PheonBest/blogs"
	veilleDefaultBranch     = "main"
	veilleDefaultConfigPath = "digests/config.json"
)

var veilleAllowedSteps = map[string]bool{"1": true, "2": true, "3": true, "7": true}
var veilleDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

var veilleHTTPClient = &http.Client{Timeout: 10 * time.Second}

func veilleEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func veilleRepo() string   { return veilleEnv("VEILLE_REPO", veilleDefaultRepo) }
func veilleBranch() string { return veilleEnv("VEILLE_BRANCH", veilleDefaultBranch) }

func veilleRawURL(repo, branch, path string) string {
	return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", repo, branch, path)
}

// --- digest schema ---------------------------------------------------------

type veilleRun struct {
	T    string `json:"t,omitempty"`
	Cite int    `json:"cite,omitempty"`
}

type veilleSection struct {
	Name       string        `json:"name"`
	Paragraphs [][]veilleRun `json:"paragraphs"`
}

type veilleSource struct {
	N     int    `json:"n"`
	Title string `json:"title"`
	URL   string `json:"url"`
	Feed  string `json:"feed"`
}

type veilleEntry struct {
	Date     string          `json:"date"`
	Label    string          `json:"label"`
	Title    string          `json:"title"`
	Sections []veilleSection `json:"sections"`
	Sources  []veilleSource  `json:"sources"`
}

type veilleIndexEntry struct {
	Date  string `json:"date"`
	Label string `json:"label"`
	Title string `json:"title"`
}

type veilleIndex struct {
	Entries []veilleIndexEntry `json:"entries"`
}

type veilleLatest struct {
	Entries []veilleEntry `json:"entries"`
}

type veilleConfig struct {
	StepDays int `json:"step_days"`
}

// veilleData is what GET /veille/data returns: latest.json's entries plus
// the current cadence, merged server-side so the custom-api widget (a
// single-URL fetch) can render the cadence buttons' selected state and a
// "last gen." timestamp without Glance needing to fetch two URLs itself.
type veilleData struct {
	StepDays  int           `json:"step_days"`
	UpdatedAt string        `json:"updated_at"`
	Entries   []veilleEntry `json:"entries"`
}

func veilleFetchJSON(url string, out any) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := veilleHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d for %s", resp.StatusCode, url)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// --- shared page chrome ------------------------------------------------

const veillePageCSS = `
body{background:var(--color-background);color:var(--color-text-base);font-family:Inter,-apple-system,"Segoe UI",Roboto,sans-serif;line-height:1.6;padding:20px;max-width:760px;margin:0 auto}
a{color:var(--color-primary)}
a:visited{color:var(--color-primary)}
.veille-page sup{font-size:.72em;margin:0 1px}
.veille-page sup a{text-decoration:none}
.veille-page .veille-section{margin-top:16px;margin-bottom:4px;text-transform:uppercase;font-size:.85em;opacity:.8}
.veille-page .veille-label{opacity:.7;margin-bottom:10px}
.veille-page .veille-back{display:inline-block;margin-bottom:16px}
.veille-search-form{margin-bottom:20px}
.veille-search-form input{width:100%;padding:8px;font-size:1em;background:var(--color-widget-content-border);color:inherit;border:none;border-radius:4px}
.veille-results li{margin-bottom:8px}
`

func (a *application) veillePageHead(w http.ResponseWriter, title string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>%s</title>`+
		`<link rel="stylesheet" href="%s"><style>%s</style></head><body class="veille-page">`,
		template.HTMLEscapeString(title), a.StaticAssetPath("css/bundle.css"), veillePageCSS)
}

func veillePageFoot(w http.ResponseWriter) {
	fmt.Fprint(w, `</body></html>`)
}

var veilleEntryTemplate = template.Must(template.New("veille-entry").Parse(`
<div class="size-h4 color-highlight">{{ .Title }}</div>
<div class="veille-label size-h6">{{ .Label }}</div>
{{ range .Sections }}
{{ if .Name }}<div class="veille-section size-h6 color-highlight">{{ .Name }}</div>{{ end }}
{{ range .Paragraphs }}
<p>{{ range . }}{{ if .Cite }}<sup><a class="color-primary-if-not-visited" href="#src-{{ .Cite }}">{{ .Cite }}</a></sup>{{ else }}{{ .T }}{{ end }}{{ end }}</p>
{{ end }}
{{ end }}
{{ if .Sources }}
<div class="size-h6 color-base" style="margin-top:10px">Sources</div>
<ol class="list list-gap-2">
{{ range .Sources }}<li id="src-{{ .N }}"><a class="color-primary-if-not-visited" href="{{ .URL }}" target="_blank" rel="noreferrer">{{ .Title }}</a> <span class="color-base size-h6">· {{ .Feed }}</span></li>{{ end }}
</ol>
{{ end }}
`))

// --- /veille/step/{n} --------------------------------------------------

func (a *application) handleVeilleStep(w http.ResponseWriter, r *http.Request) {
	n := r.PathValue("n")
	if !veilleAllowedSteps[n] {
		http.Error(w, "invalid step (must be 1, 2, 3 or 7)", http.StatusBadRequest)
		return
	}

	token := os.Getenv("VEILLE_GITHUB_TOKEN")
	if token == "" {
		http.Error(w, "veille cadence control is not configured (VEILLE_GITHUB_TOKEN unset)", http.StatusServiceUnavailable)
		return
	}

	repo := veilleRepo()
	branch := veilleBranch()
	path := veilleEnv("VEILLE_CONFIG_PATH", veilleDefaultConfigPath)
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/contents/%s?ref=%s", repo, path, branch)

	getReq, _ := http.NewRequest("GET", apiURL, nil)
	getReq.Header.Set("Authorization", "Bearer "+token)
	getReq.Header.Set("Accept", "application/vnd.github+json")
	getResp, err := veilleHTTPClient.Do(getReq)
	if err != nil {
		http.Error(w, "fetching current config: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(getResp.Body)
		http.Error(w, fmt.Sprintf("fetching current config: status %d: %s", getResp.StatusCode, string(body)), http.StatusBadGateway)
		return
	}
	var current struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&current); err != nil {
		http.Error(w, "decoding current config: "+err.Error(), http.StatusBadGateway)
		return
	}

	newContent := fmt.Sprintf("{\n \"step_days\": %s\n}\n", n)
	putBody, _ := json.Marshal(map[string]any{
		"message": fmt.Sprintf("chore(veille): set cadence to %s day(s)", n),
		"content": base64.StdEncoding.EncodeToString([]byte(newContent)),
		"sha":     current.SHA,
		"branch":  branch,
	})
	putReq, _ := http.NewRequest("PUT", fmt.Sprintf("https://api.github.com/repos/%s/contents/%s", repo, path), bytes.NewReader(putBody))
	putReq.Header.Set("Authorization", "Bearer "+token)
	putReq.Header.Set("Accept", "application/vnd.github+json")
	putReq.Header.Set("Content-Type", "application/json")
	putResp, err := veilleHTTPClient.Do(putReq)
	if err != nil {
		http.Error(w, "updating config: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK && putResp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(putResp.Body)
		http.Error(w, fmt.Sprintf("updating config: status %d: %s", putResp.StatusCode, string(body)), http.StatusBadGateway)
		return
	}

	redirectTo := r.Header.Get("Referer")
	if redirectTo == "" {
		redirectTo = a.Config.Server.BaseURL + "/feeds"
	}
	http.Redirect(w, r, redirectTo, http.StatusFound)
}

// --- /veille/search ------------------------------------------------------

func (a *application) handleVeilleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	repo, branch := veilleRepo(), veilleBranch()

	a.veillePageHead(w, "Recherche — Veille tech")
	fmt.Fprintf(w, `<a class="veille-back" href="%s/feeds">&larr; Retour</a>`, a.Config.Server.BaseURL)
	fmt.Fprint(w, `<form class="veille-search-form" method="GET" action="search">`+
		`<input type="text" name="q" placeholder="Rechercher dans les récaps…" value="`+template.HTMLEscapeString(q)+`" autofocus></form>`)

	if q == "" {
		fmt.Fprint(w, `<p class="color-base">Tapez un terme puis Entrée.</p>`)
		veillePageFoot(w)
		return
	}

	needle := strings.ToLower(q)
	matched := map[string]veilleIndexEntry{}

	var index veilleIndex
	if err := veilleFetchJSON(veilleRawURL(repo, branch, "digests/index.json"), &index); err == nil {
		for _, e := range index.Entries {
			if strings.Contains(strings.ToLower(e.Title), needle) {
				matched[e.Date] = e
			}
		}
	}

	var latest veilleLatest
	if err := veilleFetchJSON(veilleRawURL(repo, branch, "digests/latest.json"), &latest); err == nil {
		for _, e := range latest.Entries {
			if _, ok := matched[e.Date]; ok {
				continue
			}
			if veilleEntryMatches(e, needle) {
				matched[e.Date] = veilleIndexEntry{Date: e.Date, Label: e.Label, Title: e.Title}
			}
		}
	}

	if len(matched) == 0 {
		fmt.Fprint(w, `<p class="color-base">Aucun résultat.</p>`)
		veillePageFoot(w)
		return
	}

	dates := make([]string, 0, len(matched))
	for d := range matched {
		dates = append(dates, d)
	}
	// newest first
	for i := 0; i < len(dates); i++ {
		for j := i + 1; j < len(dates); j++ {
			if dates[j] > dates[i] {
				dates[i], dates[j] = dates[j], dates[i]
			}
		}
	}

	fmt.Fprint(w, `<ul class="veille-results list">`)
	for _, d := range dates {
		e := matched[d]
		fmt.Fprintf(w, `<li><a class="color-primary-if-not-visited" href="%s/veille/digest/%s">%s — %s</a></li>`,
			a.Config.Server.BaseURL, template.HTMLEscapeString(e.Date), template.HTMLEscapeString(e.Date), template.HTMLEscapeString(e.Title))
	}
	fmt.Fprint(w, `</ul>`)
	veillePageFoot(w)
}

func veilleEntryMatches(e veilleEntry, needle string) bool {
	if strings.Contains(strings.ToLower(e.Title), needle) {
		return true
	}
	for _, sec := range e.Sections {
		if strings.Contains(strings.ToLower(sec.Name), needle) {
			return true
		}
		for _, para := range sec.Paragraphs {
			for _, run := range para {
				if run.T != "" && strings.Contains(strings.ToLower(run.T), needle) {
					return true
				}
			}
		}
	}
	for _, src := range e.Sources {
		if strings.Contains(strings.ToLower(src.Title), needle) || strings.Contains(strings.ToLower(src.Feed), needle) {
			return true
		}
	}
	return false
}

// --- /veille/digest/{date} -----------------------------------------------

func (a *application) handleVeilleDigest(w http.ResponseWriter, r *http.Request) {
	date := r.PathValue("date")
	if !veilleDatePattern.MatchString(date) {
		http.Error(w, "invalid date", http.StatusBadRequest)
		return
	}

	var entry veilleEntry
	url := veilleRawURL(veilleRepo(), veilleBranch(), fmt.Sprintf("digests/%s.json", date))
	if err := veilleFetchJSON(url, &entry); err != nil {
		a.veillePageHead(w, "Récap introuvable")
		fmt.Fprintf(w, `<a class="veille-back" href="%s/feeds">&larr; Retour</a><p class="color-base">Aucun récap pour le %s.</p>`,
			a.Config.Server.BaseURL, template.HTMLEscapeString(date))
		veillePageFoot(w)
		return
	}

	a.veillePageHead(w, entry.Title)
	fmt.Fprintf(w, `<a class="veille-back" href="%s/feeds">&larr; Retour</a>`, a.Config.Server.BaseURL)
	_ = veilleEntryTemplate.Execute(w, entry)
	veillePageFoot(w)
}

// --- /veille/data ----------------------------------------------------------

func (a *application) handleVeilleData(w http.ResponseWriter, r *http.Request) {
	repo, branch := veilleRepo(), veilleBranch()

	var cfg veilleConfig
	cfg.StepDays = 1
	_ = veilleFetchJSON(veilleRawURL(repo, branch, "digests/config.json"), &cfg)

	var latest veilleLatest
	if err := veilleFetchJSON(veilleRawURL(repo, branch, "digests/latest.json"), &latest); err != nil {
		http.Error(w, "fetching digests: "+err.Error(), http.StatusBadGateway)
		return
	}

	updatedAt := ""
	if len(latest.Entries) > 0 {
		updatedAt = latest.Entries[0].Date
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(veilleData{
		StepDays:  cfg.StepDays,
		UpdatedAt: updatedAt,
		Entries:   latest.Entries,
	})
}

// --- /veille/regenerate ------------------------------------------------

func (a *application) handleVeilleRegenerate(w http.ResponseWriter, r *http.Request) {
	token := os.Getenv("VEILLE_GITHUB_TOKEN")
	if token == "" {
		http.Error(w, "veille regenerate is not configured (VEILLE_GITHUB_TOKEN unset)", http.StatusServiceUnavailable)
		return
	}

	repo := veilleRepo()
	workflow := veilleEnv("VEILLE_WORKFLOW_FILE", "veille-summary.yaml")
	branch := veilleBranch()

	body, _ := json.Marshal(map[string]any{"ref": branch})
	req, _ := http.NewRequest("POST",
		fmt.Sprintf("https://api.github.com/repos/%s/actions/workflows/%s/dispatches", repo, workflow),
		bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := veilleHTTPClient.Do(req)
	if err != nil {
		http.Error(w, "triggering regeneration: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		http.Error(w, fmt.Sprintf("triggering regeneration: status %d: %s", resp.StatusCode, string(respBody)), http.StatusBadGateway)
		return
	}

	redirectTo := r.Header.Get("Referer")
	if redirectTo == "" {
		redirectTo = a.Config.Server.BaseURL + "/feeds"
	}
	http.Redirect(w, r, redirectTo, http.StatusFound)
}
