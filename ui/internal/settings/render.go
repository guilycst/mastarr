package settings

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (h *Handler) renderConfiguration(w http.ResponseWriter, query queryState, value Configuration) {
	p := pageWriter{w: w, status: 200, title: "Settings"}
	p.start()
	p.text("<main id=\"settings-content\" aria-labelledby=\"settings-title\"><h1 id=\"settings-title\">Settings</h1><p>Configuration is observed through the API. YAML-owned records are immutable here; API-owned changes require the API's revision and If-Match checks.</p>")
	writeSource(&p, value.Source)
	detailTerm(&p, "Key source", known(value.KeySource))
	detailTerm(&p, "Configuration ETag", known(value.ETag))
	if value.RestartRequired != nil {
		required := "false"
		if *value.RestartRequired {
			required = "true"
		}
		detailTerm(&p, "Restart required", required)
	} else {
		detailTerm(&p, "Restart required", "unknown")
	}
	p.text("<section aria-labelledby=\"settings-links\"><h2 id=\"settings-links\">Configuration resources</h2><ul>")
	linkCount(&p, "/connections", "Connections", len(value.Connections))
	linkCount(&p, "/storage-roots", "Storage roots", len(value.StorageRoots))
	linkCount(&p, "/path-mappings", "Path mappings", len(value.PathMappings))
	p.text("</ul></section>")
	p.text("<section aria-labelledby=\"settings-draft\"><h2 id=\"settings-draft\">API-owned draft context</h2><p>GET fields preserve a deep link and operator context only. This page does not submit configuration changes or credentials.</p><form method=\"get\" action=\"/settings\"><fieldset><legend>Revision context</legend>")
	writeInput(&p, "ifMatch", "If-Match ETag", query.value("ifMatch", value.ETag))
	writeInput(&p, "idempotencyKey", "Idempotency key", query.value("idempotencyKey", ""))
	writeInput(&p, "operator", "Operator context", query.value("operator", ""))
	p.text("<button type=\"submit\">Keep draft in URL</button></fieldset></form></section>")
	p.text("</main></body></html>")
	p.finish()
}

func (h *Handler) renderConnectionList(w http.ResponseWriter, query queryState, page ConnectionPage) {
	p := pageWriter{w: w, status: 200, title: "Connections"}
	p.start()
	p.text("<main id=\"settings-content\" aria-labelledby=\"settings-title\"><p><a href=\"/settings")
	p.value(query.listEncoded())
	p.text("\">Back to Settings</a></p><h1 id=\"settings-title\">Connections</h1><p>Credential state is metadata only. Secret values and writeOnly fields are never returned or rendered.</p><table><caption>Configured instances</caption><thead><tr><th scope=\"col\">Kind</th><th scope=\"col\">ID</th><th scope=\"col\">Label</th><th scope=\"col\">Endpoint</th><th scope=\"col\">Health</th><th scope=\"col\">Credential state</th><th scope=\"col\">Revision</th></tr></thead><tbody>")
	if len(page.Items) == 0 {
		p.text("<tr><td colspan=\"7\">No connections on this page; incomplete coverage does not prove absence.</td></tr>")
	}
	for _, item := range page.Items {
		p.text("<tr><th scope=\"row\">")
		p.value(known(item.Kind))
		p.text("</th><td><a href=\"/connections/")
		p.value(url.PathEscape(item.ID))
		p.value(query.encodedDraft("connections"))
		p.text("\">")
		p.value(known(item.ID))
		p.text("</a></td><td>")
		p.value(known(item.Label))
		p.text("</td><td>")
		p.value(known(item.Endpoint))
		p.text("</td><td>")
		p.value(known(item.Health))
		p.text("</td><td>")
		p.value(known(item.CredentialState))
		p.text("</td><td>")
		p.value(known(item.Revision))
		p.text("</td></tr>")
	}
	p.text("</tbody></table>")
	writeNext(&p, query, page.Page.NextCursor)
	p.text("</main></body></html>")
	p.finish()
}

func (h *Handler) renderConnection(w http.ResponseWriter, query queryState, item Connection) {
	p := pageWriter{w: w, status: 200, title: "Connection detail"}
	p.start()
	p.text("<main id=\"settings-content\" aria-labelledby=\"settings-title\"><p><a href=\"/connections")
	p.value(query.listEncoded())
	p.text("\">Back to Connections</a></p><h1 id=\"settings-title\">Connection ")
	p.value(known(item.ID))
	p.text("</h1><p>API-owned edits use the displayed revision and If-Match value. The visible form is a GET draft and cannot save or retire this instance.</p><dl>")
	detailTerm(&p, "Kind", known(item.Kind))
	detailTerm(&p, "Label", known(item.Label))
	detailTerm(&p, "Endpoint", known(item.Endpoint))
	detailTerm(&p, "Health", known(item.Health))
	detailTerm(&p, "Credential state", known(item.CredentialState))
	detailTerm(&p, "Observed version", known(item.ObservedVersion))
	detailTerm(&p, "Capabilities", joinOrUnknown(item.Capabilities))
	detailTerm(&p, "Retired at", optionalTimeLabel(item.RetiredAt))
	detailTerm(&p, "Revision", known(item.Revision))
	detailTerm(&p, "ETag", known(item.ETag))
	p.text("</dl>")
	writeSource(&p, item.Source)
	writeDraft(&p, "/connections/"+url.PathEscape(item.ID), query, "Connection draft", map[string]string{
		"label": item.Label, "endpoint": item.Endpoint, "ifMatch": item.ETag,
	})
	p.text("</main></body></html>")
	p.finish()
}

func (h *Handler) renderStorageRootList(w http.ResponseWriter, query queryState, page StorageRootPage) {
	p := pageWriter{w: w, status: 200, title: "Storage roots"}
	p.start()
	p.text("<main id=\"settings-content\" aria-labelledby=\"settings-title\"><p><a href=\"/settings")
	p.value(query.listEncoded())
	p.text("\">Back to Settings</a></p><h1 id=\"settings-title\">Storage roots</h1><p>Root paths are configuration observations. Action targets continue to use rootId plus relativePath.</p><table><caption>Configured roots</caption><thead><tr><th scope=\"col\">ID</th><th scope=\"col\">Label</th><th scope=\"col\">Purpose</th><th scope=\"col\">Permission</th><th scope=\"col\">Watch</th><th scope=\"col\">Source</th></tr></thead><tbody>")
	if len(page.Items) == 0 {
		p.text("<tr><td colspan=\"6\">No roots on this page; incomplete coverage does not prove absence.</td></tr>")
	}
	for _, item := range page.Items {
		p.text("<tr><th scope=\"row\"><a href=\"/storage-roots/")
		p.value(url.PathEscape(item.ID))
		p.value(query.encodedDraft("storage-roots"))
		p.text("\">")
		p.value(known(item.ID))
		p.text("</a></th><td>")
		p.value(known(item.Label))
		p.text("</td><td>")
		p.value(known(item.Purpose))
		p.text("</td><td>")
		p.value(known(item.Permission))
		p.text("</td><td>")
		p.value(watchLabel(item))
		p.text("</td><td>")
		p.value(known(item.Source.Source))
		p.text("</td></tr>")
	}
	p.text("</tbody></table>")
	writeNext(&p, query, page.Page.NextCursor)
	p.text("</main></body></html>")
	p.finish()
}

func (h *Handler) renderStorageRoot(w http.ResponseWriter, query queryState, item StorageRoot) {
	p := pageWriter{w: w, status: 200, title: "Storage root detail"}
	p.start()
	p.text("<main id=\"settings-content\" aria-labelledby=\"settings-title\"><p><a href=\"/storage-roots")
	p.value(query.listEncoded())
	p.text("\">Back to Storage roots</a></p><h1 id=\"settings-title\">Storage root ")
	p.value(known(item.ID))
	p.text("</h1><p>Path ownership and permissions are API-owned observations. File-owned records are read-only here; use the documented configuration reload/restart procedure.</p><dl>")
	detailTerm(&p, "Label", known(item.Label))
	detailTerm(&p, "Purpose", known(item.Purpose))
	detailTerm(&p, "Path", known(item.Path))
	detailTerm(&p, "Permission", known(item.Permission))
	detailTerm(&p, "Capabilities", joinOrUnknown(item.Capabilities))
	detailTerm(&p, "Watch", watchLabel(item))
	detailTerm(&p, "Retired at", optionalTimeLabel(item.RetiredAt))
	detailTerm(&p, "Revision", known(item.Revision))
	detailTerm(&p, "ETag", known(item.ETag))
	p.text("</dl>")
	writeSource(&p, item.Source)
	writeDraft(&p, "/storage-roots/"+url.PathEscape(item.ID), query, "Storage root draft", map[string]string{
		"label": item.Label, "path": item.Path, "ifMatch": item.ETag, "watchEnabled": strconv.FormatBool(item.WatchEnabled), "watchIntervalSeconds": strconv.Itoa(item.WatchIntervalSeconds),
	})
	p.text("</main></body></html>")
	p.finish()
}

func (h *Handler) renderPathMappingList(w http.ResponseWriter, query queryState, page PathMappingPage) {
	p := pageWriter{w: w, status: 200, title: "Path mappings"}
	p.start()
	p.text("<main id=\"settings-content\" aria-labelledby=\"settings-title\"><p><a href=\"/settings")
	p.value(query.listEncoded())
	p.text("\">Back to Settings</a></p><h1 id=\"settings-title\">Path mappings</h1><p>Mappings are instance-scoped. The same textual prefix remains ambiguous without its connection and root identities.</p><table><caption>Configured mappings</caption><thead><tr><th scope=\"col\">ID</th><th scope=\"col\">Connection</th><th scope=\"col\">Root</th><th scope=\"col\">Source prefix</th><th scope=\"col\">Destination prefix</th><th scope=\"col\">Revision</th></tr></thead><tbody>")
	if len(page.Items) == 0 {
		p.text("<tr><td colspan=\"6\">No mappings on this page; incomplete coverage does not prove absence.</td></tr>")
	}
	for _, item := range page.Items {
		p.text("<tr><th scope=\"row\"><a href=\"/path-mappings/")
		p.value(url.PathEscape(item.ID))
		p.value(query.encodedDraft("path-mappings"))
		p.text("\">")
		p.value(known(item.ID))
		p.text("</a></th><td>")
		p.value(known(item.ConnectionID))
		p.text("</td><td>")
		p.value(known(item.RootID))
		p.text("</td><td>")
		p.value(known(item.SourcePrefix))
		p.text("</td><td>")
		p.value(known(item.DestinationPrefix))
		p.text("</td><td>")
		p.value(known(item.Revision))
		p.text("</td></tr>")
	}
	p.text("</tbody></table>")
	writeNext(&p, query, page.Page.NextCursor)
	p.text("</main></body></html>")
	p.finish()
}

func (h *Handler) renderPathMapping(w http.ResponseWriter, query queryState, item PathMapping) {
	p := pageWriter{w: w, status: 200, title: "Path mapping detail"}
	p.start()
	p.text("<main id=\"settings-content\" aria-labelledby=\"settings-title\"><p><a href=\"/path-mappings")
	p.value(query.listEncoded())
	p.text("\">Back to Path mappings</a></p><h1 id=\"settings-title\">Path mapping ")
	p.value(known(item.ID))
	p.text("</h1><p>Mapping identity includes the configured connection and root. Ambiguous prefixes remain visible and are not resolved by the UI.</p><dl>")
	detailTerm(&p, "Connection", known(item.ConnectionID))
	detailTerm(&p, "Root", known(item.RootID))
	detailTerm(&p, "Source prefix", known(item.SourcePrefix))
	detailTerm(&p, "Destination prefix", known(item.DestinationPrefix))
	detailTerm(&p, "Revision", known(item.Revision))
	detailTerm(&p, "ETag", known(item.ETag))
	p.text("</dl>")
	writeSource(&p, item.Source)
	writeDraft(&p, "/path-mappings/"+url.PathEscape(item.ID), query, "Path mapping draft", map[string]string{
		"sourcePrefix": item.SourcePrefix, "destinationPrefix": item.DestinationPrefix, "ifMatch": item.ETag,
	})
	p.text("</main></body></html>")
	p.finish()
}

func (h *Handler) renderConnectionCheckList(w http.ResponseWriter, query queryState, page ConnectionCheckPage) {
	p := pageWriter{w: w, status: 200, title: "Connection checks"}
	p.start()
	p.text("<main id=\"settings-content\" aria-labelledby=\"settings-title\"><p><a href=\"/settings")
	p.value(query.listEncoded())
	p.text("\">Back to Settings</a></p><h1 id=\"settings-title\">Connection checks</h1><p>Only sanitized error presence is shown. Raw upstream error text is never copied into HTML.</p><table><caption>Check observations</caption><thead><tr><th scope=\"col\">ID</th><th scope=\"col\">Connection</th><th scope=\"col\">State</th><th scope=\"col\">Created</th><th scope=\"col\">Error</th></tr></thead><tbody>")
	if len(page.Items) == 0 {
		p.text("<tr><td colspan=\"5\">No checks on this page.</td></tr>")
	}
	for _, item := range page.Items {
		p.text("<tr><th scope=\"row\"><a href=\"/connection-checks/")
		p.value(url.PathEscape(item.ID))
		p.text("\">")
		p.value(known(item.ID))
		p.text("</a></th><td>")
		p.value(known(item.ConnectionID))
		p.text("</td><td>")
		p.value(known(item.State))
		p.text("</td><td>")
		p.value(timeLabel(item.CreatedAt))
		p.text("</td><td>")
		if item.ErrorPresent {
			p.value("error available; inspect API-owned sanitized diagnostics")
		} else {
			p.value("none observed")
		}
		p.text("</td></tr>")
	}
	p.text("</tbody></table>")
	writeNext(&p, query, page.Page.NextCursor)
	p.text("</main></body></html>")
	p.finish()
}

func (h *Handler) renderConnectionCheck(w http.ResponseWriter, query queryState, item ConnectionCheck) {
	p := pageWriter{w: w, status: 200, title: "Connection check detail"}
	p.start()
	p.text("<main id=\"settings-content\" aria-labelledby=\"settings-title\"><p><a href=\"/connection-checks")
	p.value(query.listEncoded())
	p.text("\">Back to Connection checks</a></p><h1 id=\"settings-title\">Connection check ")
	p.value(known(item.ID))
	p.text("</h1><dl>")
	detailTerm(&p, "Connection", known(item.ConnectionID))
	detailTerm(&p, "State", known(item.State))
	detailTerm(&p, "Created at", timeLabel(item.CreatedAt))
	detailTerm(&p, "Completed at", optionalTimeLabel(item.CompletedAt))
	detailTerm(&p, "Capabilities", joinOrUnknown(item.Capabilities))
	detailTerm(&p, "Error", map[bool]string{true: "error available; inspect API-owned sanitized diagnostics", false: "none observed"}[item.ErrorPresent])
	p.text("</dl><p>Connection checks are read-only observations here. Retrying or creating a check belongs to an API-owned endpoint.</p></main></body></html>")
	p.finish()
}

func writeSource(p *pageWriter, source SourceMetadata) {
	p.text("<section aria-labelledby=\"source-metadata\"><h2 id=\"source-metadata\">Source provenance</h2><dl>")
	detailTerm(p, "Source", known(source.Source))
	editable := "false"
	if source.Editable {
		editable = "true"
	}
	detailTerm(p, "Editable", editable)
	detailTerm(p, "Document", known(source.DocumentID))
	detailTerm(p, "Revision", known(source.Revision))
	detailTerm(p, "Startup", timeLabel(source.StartupAt))
	detailTerm(p, "Reload policy", known(source.ReloadPolicy))
	p.text("</dl>")
	if source.Source == "yaml" || !source.Editable {
		p.text("<p>File-owned configuration is read-only here. Apply a reviewed YAML change and restart/reload through the documented operational path.</p>")
	}
	p.text("</section>")
}

func writeDraft(p *pageWriter, action string, query queryState, heading string, defaults map[string]string) {
	p.text("<section><h2>")
	p.value(heading)
	p.text("</h2><p>This GET form preserves a draft only. It does not call a mutation endpoint, read credentials, or bypass ETag/If-Match checks.</p><form method=\"get\" action=\"")
	p.value(action)
	p.text("\"><fieldset><legend>API-owned context</legend>")
	for _, key := range []string{"label", "endpoint", "path", "watchEnabled", "watchIntervalSeconds", "sourcePrefix", "destinationPrefix", "ifMatch"} {
		writeInput(p, key, key, query.value(key, defaults[key]))
	}
	writeInput(p, "idempotencyKey", "idempotencyKey", query.value("idempotencyKey", ""))
	writeInput(p, "operator", "operator", query.value("operator", ""))
	writeInput(p, "reason", "reason", query.value("reason", ""))
	p.text("<button type=\"submit\">Keep draft in URL</button></fieldset></form></section>")
}

func writeInput(p *pageWriter, name, label, value string) {
	p.text("<label>")
	p.value(label)
	p.text("<input name=\"")
	p.value(name)
	p.text("\" value=\"")
	p.value(value)
	p.text("\"></label>")
}

func writeNext(p *pageWriter, query queryState, cursor string) {
	if cursor == "" {
		return
	}
	p.text("<p><a rel=\"next\" href=\"?")
	p.value(query.next(cursor))
	p.text("\">Next page</a></p>")
}

func linkCount(p *pageWriter, path, label string, count int) {
	p.text("<li><a href=\"")
	p.value(path)
	p.text("\">")
	p.value(label)
	p.text("</a> (")
	p.value(countLabel(count))
	p.text(")</li>")
}

func detailTerm(p *pageWriter, term, value string) {
	p.text("<dt>")
	p.value(term)
	p.text("</dt><dd>")
	p.value(value)
	p.text("</dd>")
}

func known(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func joinOrUnknown(values []string) string {
	if len(values) == 0 {
		return "unknown"
	}
	return strings.Join(values, ", ")
}

func watchLabel(item StorageRoot) string {
	if item.WatchIntervalSeconds <= 0 {
		return "unknown"
	}
	return strconv.FormatBool(item.WatchEnabled) + " every " + strconv.Itoa(item.WatchIntervalSeconds) + "s"
}
