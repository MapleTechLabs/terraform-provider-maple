package fakeapi

import (
	"fmt"
	"net/http"
	"strings"
	"unicode"
)

// statusPage mirrors a v2 status page: its config document, password, custom
// domain and draft state, with the API's rules on visibility.
type statusPage struct {
	config      map[string]any
	hasPassword bool
	draft       bool
	hostname    any
	revision    int
	// beforePut runs once before the next config write, as a responder racing an apply.
	beforePut func(config map[string]any)
}

var defaultTheme = map[string]any{
	"preset": "default", "appearance": "auto", "accent": nil, "background": nil,
	"text": nil, "card": nil, "border": nil, "radius": "md", "font": "sans",
}

func (p *statusPage) view(id string) map[string]any {
	return map[string]any{
		"id": id, "object": "status_page", "name": p.config["name"], "slug": p.config["slug"],
		"visibility": p.config["visibility"], "has_password": p.hasPassword, "draft": p.draft,
	}
}

func (p *statusPage) domain(id string) map[string]any {
	records := []any{}
	if p.hostname != nil {
		records = append(records,
			map[string]any{"type": "CNAME", "name": p.hostname, "value": "status-domains.maple.test", "purpose": "routing"},
			map[string]any{"type": "TXT", "name": "_cf-custom-hostname." + fmt.Sprint(p.hostname), "value": "token-" + id, "purpose": "ownership"},
		)
	}
	return map[string]any{
		"object": "status_page_custom_domain", "page_id": id, "available": true,
		"cname_target": "status-domains.maple.test", "hostname": p.hostname, "dns_records": records,
	}
}

func (p *statusPage) withVersion() map[string]any {
	out := map[string]any{"version": fmt.Sprintf("v%d", p.revision)}
	for k, v := range p.config {
		out[k] = v
	}
	return out
}

// jsTrim trims what JavaScript's trim does: Go's whitespace without U+0085, plus U+FEFF.
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return (unicode.IsSpace(r) && r != 0x85) || r == 0xFEFF })
}

func textOrNull(v any) any {
	if s, ok := v.(string); ok && jsTrim(s) != "" {
		return jsTrim(s)
	}
	return nil
}

// apply stores a config document the way the API normalizes it.
func (api *API) applyConfig(id string, page *statusPage, doc map[string]any) (int, string) {
	visibility := page.config["visibility"]
	if v, ok := doc["visibility"]; ok {
		visibility = v
	}
	// As the API, the rules apply only to a visibility change.
	changed := visibility != page.config["visibility"]
	if changed && visibility == "password" && !page.hasPassword {
		return http.StatusBadRequest, "Set a page password before making the page password-protected"
	}
	if changed && visibility == "internal" && page.hostname != nil {
		return http.StatusBadRequest, "Remove the custom domain before making the page internal"
	}
	existing := map[any]bool{}
	for _, raw := range page.config["components"].([]any) {
		existing[raw.(map[string]any)["id"]] = true
	}
	theme := map[string]any{}
	for k, v := range defaultTheme {
		theme[k] = v
	}
	if t, ok := doc["theme"].(map[string]any); ok {
		for k, v := range t {
			theme[k] = v
		}
	}
	components := []any{}
	listed, _ := doc["components"].([]any)
	for _, raw := range listed {
		c := raw.(map[string]any)
		cid, hasID := c["id"]
		if hasID && !existing[cid] {
			return http.StatusBadRequest, fmt.Sprintf("Component %v is not on this page", cid)
		}
		if !hasID {
			api.next++
			cid = fmt.Sprintf("stc_%d", api.next)
		}
		showUptime, ok := c["show_uptime"].(bool)
		if !ok {
			showUptime = true
		}
		components = append(components, map[string]any{
			"id": cid, "name": c["name"], "description": textOrNull(c["description"]), "group": textOrNull(c["group"]),
			"monitor_id": c["monitor_id"], "manual_status": c["manual_status"], "show_uptime": showUptime,
		})
	}
	showPoweredBy, ok := doc["show_powered_by"].(bool)
	if !ok {
		showPoweredBy = true
	}
	autoIncidents, ok := doc["auto_incidents"].(string)
	if !ok {
		autoIncidents = "off"
	}
	config := map[string]any{
		"name": doc["name"], "slug": doc["slug"], "show_powered_by": showPoweredBy, "auto_incidents": autoIncidents,
		"visibility": visibility, "theme": theme, "components": components,
	}
	for _, k := range []string{"description", "website_url", "logo_url", "logo_dark_url", "favicon_url", "support_url", "privacy_url", "terms_url"} {
		config[k] = textOrNull(doc[k])
	}
	page.config = config
	return 0, ""
}

func (api *API) handleStatusPage(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if r.URL.Path == "/v2/status_pages" && r.Method == http.MethodPost {
		api.next++
		id := fmt.Sprintf("stp_%d", api.next)
		page := &statusPage{
			config: map[string]any{"visibility": "public", "components": []any{}},
			draft:  body["publish"] == false,
		}
		if status, message := api.applyConfig(id, page, body); status != 0 {
			writeError(w, status, "invalid_request_error", message)
			return
		}
		api.pages[id] = page
		writeJSON(w, page.view(id))
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v2/status_pages/")
	id, sub, _ := strings.Cut(rest, "/")
	page, ok := api.pages[id]
	if !ok {
		writeError(w, http.StatusNotFound, "not_found_error", "Status page not found")
		return
	}
	switch r.Method + " " + sub {
	case "GET ":
		writeJSON(w, page.view(id))
	case "DELETE ":
		delete(api.pages, id)
		writeJSON(w, map[string]any{"id": id, "object": "status_page", "deleted": true})
	case "GET config":
		writeJSON(w, page.withVersion())
	case "PUT config":
		if page.beforePut != nil {
			page.beforePut(page.config)
			page.beforePut = nil
			page.revision++
		}
		if v := r.URL.Query().Get("if_version"); v != "" && v != fmt.Sprintf("v%d", page.revision) {
			writeError(w, http.StatusConflict, "conflict_error", "The page's config changed since it was read")
			return
		}
		if status, message := api.applyConfig(id, page, body); status != 0 {
			writeError(w, status, "invalid_request_error", message)
			return
		}
		page.revision++
		writeJSON(w, page.withVersion())
	case "PUT password":
		page.hasPassword = true
		writeJSON(w, page.view(id))
	case "DELETE password":
		if page.config["visibility"] == "password" {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "Make the page public or internal before removing its password")
			return
		}
		page.hasPassword = false
		writeJSON(w, page.view(id))
	case "GET custom_domain":
		writeJSON(w, page.domain(id))
	case "PUT custom_domain":
		if page.config["visibility"] == "internal" {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "An internal page cannot have a custom domain")
			return
		}
		// As the API: trimmed, lowercased, without a trailing dot.
		page.hostname = strings.TrimSuffix(strings.ToLower(jsTrim(fmt.Sprint(body["hostname"]))), ".")
		writeJSON(w, page.domain(id))
	case "DELETE custom_domain":
		if page.hostname == nil {
			writeError(w, http.StatusNotFound, "not_found_error", "This status page has no custom domain")
			return
		}
		page.hostname = nil
		writeJSON(w, page.domain(id))
	case "POST publish":
		page.draft = false
		writeJSON(w, map[string]any{"id": id, "object": "status_page.publish_result", "outcome": "published"})
	default:
		writeError(w, http.StatusNotFound, "not_found_error", "no route")
	}
}

// StatusPage returns a status page's view, config and custom domain hostname, or nil.
func (api *API) StatusPage(id string) (view, config map[string]any, hostname any) {
	api.mu.Lock()
	defer api.mu.Unlock()
	page, ok := api.pages[id]
	if !ok {
		return nil, nil, nil
	}
	return page.view(id), page.config, page.hostname
}

// MutateStatusPage edits a status page's config out of band, as a change made in the Maple UI.
func (api *API) MutateStatusPage(id string, edit func(config map[string]any)) {
	api.mu.Lock()
	defer api.mu.Unlock()
	if page, ok := api.pages[id]; ok {
		edit(page.config)
		page.revision++
	}
}

// BeforeNextConfigWrite edits a status page's config right before the next config
// write lands, as a responder changing a status while an apply is in flight.
func (api *API) BeforeNextConfigWrite(id string, edit func(config map[string]any)) {
	api.mu.Lock()
	defer api.mu.Unlock()
	if page, ok := api.pages[id]; ok {
		page.beforePut = edit
	}
}
