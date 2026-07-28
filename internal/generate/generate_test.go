package generate

import (
	"encoding/json"
	"testing"

	"github.com/jarrod-lowe/rdk/internal/parse"
)

func demoDefs() []parse.Definition {
	return []parse.Definition{
		{Kind: "config", Name: "demo", File: "config.yaml", Attrs: map[string]any{"name": "demo"}},
		{Kind: "s3-bucket", Name: "assets", File: "assets.yaml",
			Attrs: map[string]any{"name": "assets", "description": "Static assets"}},
	}
}

func TestBuildFileSet(t *testing.T) {
	set, err := Build(demoDefs())
	if err != nil {
		t.Fatal(err)
	}
	if set.Len() == 0 {
		t.Fatal("empty file set")
	}
}

func TestTFDocPinsProviderAndModules(t *testing.T) {
	doc := tfDoc(demoDefs())
	b, _ := json.Marshal(doc)
	var round map[string]any
	json.Unmarshal(b, &round)
	tf := round["terraform"].(map[string]any)["required_providers"].(map[string]any)["aws"].(map[string]any)
	if tf["source"] != "hashicorp/aws" || tf["version"] != "~> 6.0" {
		t.Errorf("provider pin = %v", tf)
	}
	mods := round["module"].(map[string]any)
	assets := mods["assets"].(map[string]any)
	if assets["source"] != "./modules/s3-bucket" || assets["name"] != "assets" {
		t.Errorf("module call = %v", assets)
	}
}
