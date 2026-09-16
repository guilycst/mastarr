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
	}
}
