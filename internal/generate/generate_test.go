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
	doc, err := tfDoc(demoDefs())
	if err != nil {
		t.Fatal(err)
	}
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
	if len(mods) != 1 {
		t.Errorf("want exactly one module block (config produces none), got %d: %v", len(mods), mods)
	}
	if _, ok := mods["demo"]; ok {
		t.Error("config def must not produce a module block")
	}
}

func TestModuleCallFlattensInputs(t *testing.T) {
	call, err := newModuleCall("./modules/s3-bucket", map[string]any{"name": "assets"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	// Inputs sit alongside source in one object: Terraform's JSON syntax has no
	// nesting for module arguments.
	if got, want := string(b), `{"name":"assets","source":"./modules/s3-bucket"}`; got != want {
		t.Errorf("moduleCall JSON = %s, want %s", got, want)
	}
}

func TestNewModuleCallRejectsSourceInput(t *testing.T) {
	_, err := newModuleCall("./modules/s3-bucket", map[string]any{"source": "./modules/elsewhere"})
	if err == nil {
		t.Fatal("want error for an input colliding with the reserved source argument")
	}
}
