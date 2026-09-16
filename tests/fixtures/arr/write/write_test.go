package arrwritefixtures

import (
	"embed"
	"encoding/json"
	"testing"
)

// These are public-repository synthetic fixtures. They document the exact
// fields the root adapter's protocol tests exercise without containing a live
// Arr response, credential, host or media inventory.
//
//go:embed *.json
var fixtureFiles embed.FS

func TestSyntheticArrWriteFixturesAreBound(t *testing.T) {
	for _, name := range []string{"registration.json", "import.json"} {
		data, err := fixtureFiles.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var document struct {
			Upstream string `json:"upstream"`
			Expected string `json:"expected"`
		}
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		if document.Upstream == "" || document.Expected == "" {
			t.Fatalf("fixture %s lacks evidence labels: %#v", name, document)
		}
		var fields map[string]any
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatalf("decode %s fields: %v", name, err)
		}
		switch name {
		case "registration.json":
			newFields, ok := fields["new"].(map[string]any)
			if !ok || newFields["monitored"] != false || newFields["searchForMovie"] != false || newFields["searchForMissingEpisodes"] != false {
				t.Fatalf("registration fixture lost no-search defaults: %#v", newFields)
			}
		case "import.json":
			if fields["transfer"] != "copy" {
				t.Fatalf("import fixture transfer = %#v, want copy", fields["transfer"])
			}
			files, ok := fields["files"].([]any)
			if !ok || len(files) != 2 {
				t.Fatalf("import fixture files = %#v, want two exact episode rows", fields["files"])
			}
			for index, raw := range files {
				row, ok := raw.(map[string]any)
				if !ok || row["episodeId"] == nil || row["path"] == nil {
					t.Fatalf("import fixture row %d = %#v, want path and episode identity", index, raw)
				}
			}
		}
	}
}
