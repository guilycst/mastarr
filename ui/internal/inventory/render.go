package inventory

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (h *Handler) renderDiscoveryList(w http.ResponseWriter, query queryState, page DiscoveryPage) {
	writeListPageWithCount(w, "discoveries", query, "Discoveries", "Observed file groups and matching evidence. Unknown means the API has not proved a state.", len(page.Items), func(p *pageWriter) {
		p.text("<table><caption>Discovery observations</caption><thead><tr><th scope=\"col\">Identity</th><th scope=\"col\">Readiness</th><th scope=\"col\">Files</th><th scope=\"col\">Provenance</th><th scope=\"col\">Coverage</th></tr></thead><tbody>")
		if len(page.Items) == 0 {
			p.text("<tr><td colspan=\"5\">No records on this page; absence is not inferred from incomplete coverage.</td></tr>")
		}
		for _, item := range page.Items {
			p.text("<tr><th scope=\"row\"><a href=\"")
			p.value(pathFor("discoveries", item.ID) + query.encoded(false))
			p.text("\">")
			p.value(knownOrUnknown(item.ID))
			p.text("</a></th><td>")
			p.value(stateLabel(item.Readiness))
			p.text("</td><td>")
			p.value(countLabel(len(item.Files)))
			p.text("</td><td>")
			if len(item.Provenance) == 0 {
				p.value("unknown")
			} else {
				p.value(fmt.Sprintf("%d observation(s)", len(item.Provenance)))
			}
			p.text("</td><td>")
			p.value(coverageLabel(item.Coverage))
			p.text("</td></tr>")
		}
		p.text("</tbody></table>")
	}, page.Page)
}

func (h *Handler) renderMediaList(w http.ResponseWriter, query queryState, page MediaPage) {
	writeListPageWithCount(w, "media", query, "Media", "Aggregated identities with independent instance observations.", len(page.Items), func(p *pageWriter) {
		p.text("<table><caption>Media identity observations</caption><thead><tr><th scope=\"col\">Identity</th><th scope=\"col\">Kind</th><th scope=\"col\">Provider ID</th><th scope=\"col\">Tracking</th></tr></thead><tbody>")
		if len(page.Items) == 0 {
			p.text("<tr><td colspan=\"4\">No records on this page; unknown evidence remains visible in detail views.</td></tr>")
		}
		for _, item := range page.Items {
			p.text("<tr><th scope=\"row\"><a href=\"")
			p.value(pathFor("media", item.ID) + query.encoded(false))
			p.text("\">")
			p.value(knownOrUnknown(item.ID))
			p.text("</a></th><td>")
			p.value(stateLabel(item.Kind))
			p.text("</td><td>")
			p.value(knownOrUnknown(item.ProviderID))
			p.text("</td><td>")
			writeTrackingSummary(p, item.Tracking)
			p.text("</td></tr>")
		}
		p.text("</tbody></table>")
	}, page.Page)
}

func (h *Handler) renderDownloadList(w http.ResponseWriter, query queryState, page DownloadPage) {
	writeListPageWithCount(w, "downloads", query, "Downloads", "Download-client observations and provenance. Download state does not prove library availability.", len(page.Items), func(p *pageWriter) {
		p.text("<table><caption>Download observations</caption><thead><tr><th scope=\"col\">Identity</th><th scope=\"col\">Client instance</th><th scope=\"col\">State</th><th scope=\"col\">Client item</th><th scope=\"col\">Coverage</th></tr></thead><tbody>")
		if len(page.Items) == 0 {
			p.text("<tr><td colspan=\"5\">No records on this page; no client absence is inferred.</td></tr>")
		}
		for _, item := range page.Items {
			p.text("<tr><th scope=\"row\"><a href=\"")
			p.value(pathFor("downloads", item.ID) + query.encoded(false))
			p.text("\">")
			p.value(knownOrUnknown(item.ID))
			p.text("</a></th><td>")
			p.value(knownOrUnknown(item.ConnectionID))
			p.text("</td><td>")
			p.value(stateLabel(item.State))
			p.text("</td><td>")
			p.value(knownOrUnknown(item.ClientItemID))
			p.text("</td><td>")
			p.value(coverageLabel(item.Coverage))
			p.text("</td></tr>")
		}
		p.text("</tbody></table>")
	}, page.Page)
}

func (h *Handler) renderDescriptorList(w http.ResponseWriter, query queryState, page DescriptorPage) {
	writeListPageWithCount(w, "descriptors", query, "Descriptors", "Retained descriptor metadata. Original bytes are never embedded in this view.", len(page.Items), func(p *pageWriter) {
		p.text("<table><caption>Descriptor metadata</caption><thead><tr><th scope=\"col\">Identity</th><th scope=\"col\">Type</th><th scope=\"col\">Size</th><th scope=\"col\">Availability</th><th scope=\"col\">Digest</th></tr></thead><tbody>")
		if len(page.Items) == 0 {
			p.text("<tr><td colspan=\"5\">No descriptors on this page.</td></tr>")
		}
		for _, item := range page.Items {
			p.text("<tr><th scope=\"row\"><a href=\"")
			p.value(pathFor("descriptors", item.ID) + query.encoded(false))
			p.text("\">")
			p.value(knownOrUnknown(item.ID))
			p.text("</a></th><td>")
			p.value(stateLabel(item.Type))
			p.text("</td><td>")
			p.value(strconv.Itoa(item.Size))
			p.text("</td><td>")
			p.value(stateLabel(item.Availability))
			p.text("</td><td>")
			p.value(knownOrUnknown(item.Digest))
			p.text("</td></tr>")
		}
		p.text("</tbody></table>")
	}, page.Page)
}

func (h *Handler) renderDiscoveryDetail(w http.ResponseWriter, route string, query queryState, item Discovery) {
	p := newDetailWriter(w, titleForRoute(route, true), route, query, item.ID)
	p.start("Discovery detail")
	p.text("<p>Read-only observation; association inputs are local draft state and do not dispatch actions.</p><dl>")
	detailTerm(&p.pageWriter, "Identity", item.ID)
	detailTerm(&p.pageWriter, "Readiness", stateLabel(item.Readiness))
	detailTerm(&p.pageWriter, "Observed at", timeLabel(item.ObservedAt))
	p.text("</dl>")
	writeCoverageEvidence(&p.pageWriter, "Discovery coverage", item.Coverage)
	writeDiscoveryFiles(&p.pageWriter, query, item.Files)
	writeCandidates(&p.pageWriter, query, item.Candidates)
	writeProvenance(&p.pageWriter, item.Provenance)
	p.text("</main></body></html>")
	p.finish()
}

func (h *Handler) renderMediaDetail(w http.ResponseWriter, route string, query queryState, item Media) {
	p := newDetailWriter(w, titleForRoute(route, true), route, query, item.ID)
	p.start("Media detail")
	p.text("<p>Read-only observation; identity fields are editable local draft values only.</p><form method=\"get\" action=\"")
	p.value(pathFor(route, item.ID))
	p.text("\" aria-label=\"Media identity draft\"><fieldset><legend>Identity draft</legend>")
	writeHiddenQueryValues(&p.pageWriter, query, map[string]struct{}{
		"identity": {}, "providerId": {}, "kind": {}, "selection": {}, "episode": {},
		"subtitleLanguage": {}, "subtitleForced": {}, "subtitleSDH": {}, "subtitlePair": {},
	})
	writeLabeledInput(&p.pageWriter, "identity", "Display title", query.value("identity", item.Title))
	writeLabeledInput(&p.pageWriter, "providerId", "Provider ID", query.value("providerId", item.ProviderID))
	writeLabeledSelect(&p.pageWriter, "kind", "Media kind", query.value("kind", item.Kind), []string{"movie", "episode", "season", "anime"}, true)
	writeLabeledInput(&p.pageWriter, "selection", "Selection", query.value("selection", ""))
	writeLabeledInput(&p.pageWriter, "episode", "Episode", query.value("episode", ""))
	writeLabeledInput(&p.pageWriter, "subtitleLanguage", "Subtitle language", query.value("subtitleLanguage", ""))
	writeLabeledInput(&p.pageWriter, "subtitleForced", "Subtitle forced", query.value("subtitleForced", ""))
	writeLabeledInput(&p.pageWriter, "subtitleSDH", "Subtitle SDH", query.value("subtitleSDH", ""))
	writeLabeledInput(&p.pageWriter, "subtitlePair", "Subtitle pair", query.value("subtitlePair", ""))
	p.text("<button type=\"submit\">Keep draft in URL</button></fieldset></form><dl>")
	detailTerm(&p.pageWriter, "Identity", item.ID)
	detailTerm(&p.pageWriter, "Kind", stateLabel(item.Kind))
	detailTerm(&p.pageWriter, "Provider ID", knownOrUnknown(item.ProviderID))
	detailTerm(&p.pageWriter, "Observed at", timeLabel(item.ObservedAt))
	p.text("</dl><section aria-labelledby=\"tracking-title\"><h2 id=\"tracking-title\">Per-instance tracking</h2>")
	writeTrackingTable(p, item.Tracking)
	if len(item.DiscoveryIDs) > 0 {
		p.text("<h2>Source discoveries</h2><ul>")
		for _, id := range item.DiscoveryIDs {
			p.text("<li>")
			p.value(knownOrUnknown(id))
			p.text("</li>")
		}
		p.text("</ul>")
	} else {
		p.text("<p>Source discoveries: unknown.</p>")
	}
	p.text("</section></main></body></html>")
	p.finish()
}

func (h *Handler) renderDownloadDetail(w http.ResponseWriter, route string, query queryState, item Download) {
	p := newDetailWriter(w, titleForRoute(route, true), route, query, item.ID)
	p.start("Download detail")
	p.text("<p>Read-only client provenance. Download state does not establish registration or availability.</p><dl>")
	detailTerm(&p.pageWriter, "Identity", item.ID)
	detailTerm(&p.pageWriter, "Client instance", item.ConnectionID)
	detailTerm(&p.pageWriter, "Client item", item.ClientItemID)
	detailTerm(&p.pageWriter, "State", stateLabel(item.State))
	detailTerm(&p.pageWriter, "Hash", item.Hash)
	detailTerm(&p.pageWriter, "NZB ID", item.NzbID)
	detailTerm(&p.pageWriter, "Deprecated ID", item.DeprecatedID)
	detailTerm(&p.pageWriter, "Descriptor ID", item.DescriptorID)
	detailTerm(&p.pageWriter, "Source path", item.SourcePath)
	detailTerm(&p.pageWriter, "Observed at", timeLabel(item.ObservedAt))
	detailTerm(&p.pageWriter, "Completed at", optionalTimeLabel(item.CompletedAt))
	p.text("</dl>")
	writeCoverageEvidence(&p.pageWriter, "Download coverage", item.Coverage)
	p.text("</main></body></html>")
	p.finish()
}

func (h *Handler) renderDescriptorDetail(w http.ResponseWriter, route string, query queryState, item Descriptor) {
	p := newDetailWriter(w, titleForRoute(route, true), route, query, item.ID)
	p.start("Descriptor detail")
	p.text("<p>Descriptor content is intentionally excluded from the HTML response.</p><dl>")
	detailTerm(&p.pageWriter, "Identity", item.ID)
	detailTerm(&p.pageWriter, "Type", stateLabel(item.Type))
	detailTerm(&p.pageWriter, "Size", strconv.Itoa(item.Size))
	detailTerm(&p.pageWriter, "Availability", stateLabel(item.Availability))
	detailTerm(&p.pageWriter, "Digest", item.Digest)
	detailTerm(&p.pageWriter, "Source", item.Source)
	detailTerm(&p.pageWriter, "Captured at", timeLabel(item.CapturedAt))
	detailTerm(&p.pageWriter, "Retention until", optionalTimeLabel(item.RetentionUntil))
	p.text("</dl></main></body></html>")
	p.finish()
}

type detailWriter struct {
	pageWriter
	route string
	query queryState
	id    string
}

func newDetailWriter(w http.ResponseWriter, title, route string, query queryState, id string) *detailWriter {
	return &detailWriter{pageWriter: pageWriter{w: w, status: http.StatusOK, title: title}, route: route, query: query, id: id}
}

func (p *detailWriter) start(heading string) {
	p.pageWriter.start()
	p.text("<main id=\"inventory-content\" aria-labelledby=\"inventory-title\"><p><a href=\"")
	// Detail-only draft fields belong to this detail flow. The list parser
	// intentionally rejects them, so the back link carries only the route's
	// validated list filters and pagination context.
	p.value("/" + p.route + p.query.listEncoded(p.route))
	p.text("\">Back to ")
	p.value(titleForRoute(p.route, false))
	p.text("</a></p><h1 id=\"inventory-title\">")
	p.value(heading)
	p.text("</h1>")
}

func detailTerm(p *pageWriter, term, value string) {
	p.text("<dt>")
	p.value(term)
	p.text("</dt><dd>")
	p.value(knownOrUnknown(value))
	p.text("</dd>")
}

func writeDiscoveryFiles(p *pageWriter, query queryState, files []File) {
	p.text("<section aria-labelledby=\"files-title\"><h2 id=\"files-title\">Files and associations</h2>")
	if len(files) == 0 {
		p.text("<p>Files: unknown.</p></section>")
		return
	}
	p.text("<form method=\"get\" aria-label=\"Editable file associations\"><p>These fields are local drafts. No write is sent by this view.</p>")
	excluded := make(map[string]struct{}, len(files)*7)
	for index := range files {
		prefix := fmt.Sprintf("association-%d-", index)
		for _, field := range []string{"identity", "episode", "language", "pair", "forced", "sdh", "role"} {
			excluded[prefix+field] = struct{}{}
		}
	}
	writeHiddenQueryValues(p, query, excluded)
	for index, file := range files {
		prefix := fmt.Sprintf("association-%d-", index)
		p.text("<fieldset><legend>File ")
		p.value(strconv.Itoa(index + 1))
		p.text(": ")
		p.value(knownOrUnknown(file.RelativePath))
		p.text("</legend><dl>")
		detailTerm(p, "Path", file.RelativePath)
		detailTerm(p, "Type", file.Type)
		detailTerm(p, "Role", file.Role)
		detailTerm(p, "Size", strconv.Itoa(file.Size))
		detailTerm(p, "Digest", file.Digest)
		detailTerm(p, "File identity", file.FileIdentity)
		detailTerm(p, "Observed at", optionalTimeLabel(file.ObservedAt))
		p.text("</dl>")
		writeLabeledInput(p, prefix+"identity", "Identity", query.value(prefix+"identity", file.FileIdentity))
		writeLabeledInput(p, prefix+"episode", "Episode or season", query.value(prefix+"episode", ""))
		writeLabeledInput(p, prefix+"language", "Subtitle language", query.value(prefix+"language", ""))
		writeLabeledInput(p, prefix+"pair", "Subtitle pair", query.value(prefix+"pair", ""))
		writeLabeledInput(p, prefix+"forced", "Forced flag", query.value(prefix+"forced", ""))
		writeLabeledInput(p, prefix+"sdh", "SDH flag", query.value(prefix+"sdh", ""))
		p.text("<label for=\"")
		p.value(prefix + "role")
		p.text("\">Role association</label><select id=\"")
		p.value(prefix + "role")
		p.text("\" name=\"")
		p.value(prefix + "role")
		p.text("\">")
		p.text("<option value=\"\"")
		if query.value(prefix+"role", file.Role) == "" {
			p.text(" selected")
		}
		p.text(">Unknown</option>")
		for _, role := range []string{"video", "subtitle", "companion"} {
			p.text("<option value=\"")
			p.value(role)
			p.text("\"")
			if query.value(prefix+"role", file.Role) == role {
				p.text(" selected")
			}
			p.text(">")
			p.value(role)
			p.text("</option>")
		}
		p.text("</select></fieldset>")
	}
	p.text("<button type=\"submit\">Keep associations in URL</button></form></section>")
}

func writeCandidates(p *pageWriter, query queryState, candidates []Candidate) {
	p.text("<section aria-labelledby=\"candidates-title\"><h2 id=\"candidates-title\">Identity suggestions</h2>")
	if len(candidates) == 0 {
		p.text("<p>Candidate evidence: unknown.</p></section>")
		return
	}
	p.text("<form method=\"get\" aria-label=\"Editable identity suggestions\"><p>Suggestions are evidence for review and are not approvals.</p>")
	excluded := make(map[string]struct{}, len(candidates)*8)
	for index := range candidates {
		prefix := fmt.Sprintf("candidate-%d-", index)
		for _, field := range []string{"title", "provider", "external", "kind", "season", "episodes", "year", "score"} {
			excluded[prefix+field] = struct{}{}
		}
	}
	writeHiddenQueryValues(p, query, excluded)
	for index, candidate := range candidates {
		prefix := fmt.Sprintf("candidate-%d-", index)
		p.text("<fieldset><legend>Suggestion ")
		p.value(strconv.Itoa(index + 1))
		p.text("</legend>")
		writeLabeledInput(p, prefix+"title", "Title", query.value(prefix+"title", candidate.Title))
		writeLabeledInput(p, prefix+"provider", "Provider ID", query.value(prefix+"provider", candidate.ProviderID))
		writeLabeledInput(p, prefix+"external", "External ID", query.value(prefix+"external", candidate.ExternalID))
		writeLabeledSelect(p, prefix+"kind", "Kind", query.value(prefix+"kind", candidate.Kind), []string{"movie", "episode", "season", "anime"}, false)
		writeLabeledInput(p, prefix+"season", "Season", query.value(prefix+"season", intPointerValue(candidate.Season)))
		writeLabeledInput(p, prefix+"episodes", "Episodes", query.value(prefix+"episodes", joinInts(candidate.Episodes)))
		writeLabeledInput(p, prefix+"year", "Year", query.value(prefix+"year", intPointerValue(candidate.Year)))
		writeLabeledInput(p, prefix+"score", "Score", query.value(prefix+"score", floatPointerValue(candidate.Score)))
		p.text("</fieldset>")
	}
	p.text("<button type=\"submit\">Keep suggestions in URL</button></form></section>")
}

func writeProvenance(p *pageWriter, provenance []Provenance) {
	p.text("<section aria-labelledby=\"provenance-title\"><h2 id=\"provenance-title\">Download provenance</h2>")
	if len(provenance) == 0 {
		p.text("<p>Provenance: unknown.</p></section>")
		return
	}
	p.text("<ul>")
	for _, item := range provenance {
		p.text("<li><strong>Instance:</strong> ")
		p.value(knownOrUnknown(item.ConnectionID))
		p.text("; <strong>client item:</strong> ")
		p.value(knownOrUnknown(item.ClientItemID))
		p.text("; <strong>descriptor:</strong> ")
		p.value(knownOrUnknown(item.DescriptorID))
		p.text("; <strong>hash:</strong> ")
		p.value(knownOrUnknown(item.Hash))
		p.text("; <strong>completed at:</strong> ")
		p.value(optionalTimeLabel(item.CompletedAt))
		p.text("; <strong>source:</strong> ")
		p.value(knownOrUnknown(item.SourcePath))
		p.text("</li>")
	}
	p.text("</ul></section>")
}

func writeTrackingSummary(p *pageWriter, tracking []Tracking) {
	if len(tracking) == 0 {
		p.value("unknown")
		return
	}
	values := make([]string, 0, len(tracking))
	for _, item := range tracking {
		values = append(values, knownOrUnknown(item.ConnectionID)+": "+stateLabel(item.Value))
	}
	p.value(strings.Join(values, "; "))
}

func writeTrackingTable(p *detailWriter, tracking []Tracking) {
	if len(tracking) == 0 {
		p.text("<p>Tracking: unknown; no instance observation was provided.</p>")
		return
	}
	p.text("<table><caption>Per-instance tracking observations</caption><thead><tr><th scope=\"col\">Instance</th><th scope=\"col\">Dimension</th><th scope=\"col\">Value</th><th scope=\"col\">Provider ID</th><th scope=\"col\">External ID</th><th scope=\"col\">Observed at</th><th scope=\"col\">Coverage ID</th><th scope=\"col\">Evidence</th></tr></thead><tbody>")
	for _, item := range tracking {
		p.text("<tr><th scope=\"row\">")
		p.value(knownOrUnknown(item.ConnectionID))
		p.text("</th><td>")
		p.value(stateLabel(item.Dimension))
		p.text("</td><td>")
		p.value(stateLabel(item.Value))
		p.text("</td><td>")
		p.value(knownOrUnknown(item.ProviderID))
		p.text("</td><td>")
		p.value(knownOrUnknown(item.ExternalID))
		p.text("</td><td>")
		p.value(timeLabel(item.ObservedAt))
		p.text("</td><td>")
		p.value(knownOrUnknown(item.CoverageID))
		p.text("</td><td>")
		if len(item.Evidence) == 0 {
			p.value("unknown")
		} else {
			p.value(strings.Join(item.Evidence, "; "))
		}
		p.text("</td></tr>")
	}
	p.text("</tbody></table>")
}

func writeLabeledInput(p *pageWriter, name, label, value string) {
	p.text("<label for=\"")
	p.value(name)
	p.text("\">")
	p.value(label)
	p.text("</label><input id=\"")
	p.value(name)
	p.text("\" name=\"")
	p.value(name)
	p.text("\" value=\"")
	p.value(value)
	p.text("\">")
}

func writeLabeledSelect(p *pageWriter, name, label, value string, options []string, includeUnknown bool) {
	p.text("<label for=\"")
	p.value(name)
	p.text("\">")
	p.value(label)
	p.text("</label><select id=\"")
	p.value(name)
	p.text("\" name=\"")
	p.value(name)
	p.text("\">")
	if includeUnknown {
		p.text("<option value=\"\"")
		if value == "" {
			p.text(" selected")
		}
		p.text(">Unknown</option>")
	}
	for _, option := range options {
		p.text("<option value=\"")
		p.value(option)
		p.text("\"")
		if value == option {
			p.text(" selected")
		}
		p.text(">")
		p.value(option)
		p.text("</option>")
	}
	p.text("</select>")
}

func writeHiddenQueryValues(p *pageWriter, query queryState, excluded map[string]struct{}) {
	keys := make([]string, 0, len(query.Values))
	for key := range query.Values {
		if _, skip := excluded[key]; skip {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		p.text("<input type=\"hidden\" name=\"")
		p.value(key)
		p.text("\" value=\"")
		p.value(query.Values[key])
		p.text("\">")
	}
}

func writeCoverageEvidence(p *pageWriter, title string, coverage *Coverage) {
	p.text("<section aria-labelledby=\"coverage-title\"><h2 id=\"coverage-title\">")
	p.value(title)
	p.text("</h2>")
	if coverage == nil {
		p.text("<p>Coverage: unknown.</p></section>")
		return
	}
	p.text("<dl>")
	writeCoverageTerms(p, *coverage)
	p.text("</dl></section>")
}

func timeLabel(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	return value.UTC().Format("2006-01-02T15:04:05Z07:00")
}

func optionalTimeLabel(value *time.Time) string {
	if value == nil {
		return "unknown"
	}
	return timeLabel(*value)
}

func joinInts(values []int) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}
