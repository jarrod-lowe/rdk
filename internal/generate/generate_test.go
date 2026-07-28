package generate

import (
	"bytes"
	"encoding/json"
	"testing"
	"testing/fstest"

	"github.com/jarrod-lowe/rdk/internal/parse"
)

func demoDefs() []parse.Definition {
	return []parse.Definition{
		{Kind: "config", Name: "demo", File: "config.yaml",
			Attrs: map[string]any{"name": "demo"}},
		{Kind: "s3-bucket", Name: "assets", File: "assets.yaml",
			Attrs: map[string]any{"name": "assets", "description": "Static assets"}},
	}
}

func TestBuildTree(t *testing.T) {
	tree, err := Build(demoDefs())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, want := range []string{
		"README.md",
		"terraform/main.tf.json",
		"terraform/modules/s3-bucket/main.tf",
		"terraform/modules/s3-bucket/variables.tf",
		"terraform/modules/s3-bucket/outputs.tf",
	} {
		if _, ok := tree[want]; !ok {
			t.Errorf("tree missing %s", want)
		}
	}
}

func TestTFJSONContent(t *testing.T) {
	tree, err := Build(demoDefs())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(tree["terraform/main.tf.json"], &doc); err != nil {
		t.Fatalf("main.tf.json is not JSON: %v", err)
	}
	mods := doc["module"].(map[string]any)
	assets := mods["assets"].(map[string]any)
	if assets["source"] != "./modules/s3-bucket" {
		t.Errorf("source = %v", assets["source"])
	}
	if assets["name"] != "assets" || assets["description"] != "Static assets" {
		t.Errorf("inputs not mapped: %v", assets)
	}
}

func TestVendorModulePreservesSubdirs(t *testing.T) {
	fsys := fstest.MapFS{
		"modules/demo/main.tf":      {Data: []byte("root")},
		"modules/demo/sub/nested.tf": {Data: []byte("nested")},
	}
	tree := Tree{}
	if err := vendorModuleFS(fsys, "demo", tree); err != nil {
		t.Fatal(err)
	}
	if got := string(tree["terraform/modules/demo/main.tf"]); got != "root" {
		t.Errorf("root file = %q, want root", got)
	}
	nested, ok := tree["terraform/modules/demo/sub/nested.tf"]
	if !ok {
		t.Fatalf("nested file flattened or missing; tree keys: %v", keys(tree))
	}
	if string(nested) != "nested" {
		t.Errorf("nested content = %q, want nested", string(nested))
	}
}

func TestTFJSONPinsProviderInStack(t *testing.T) {
	tree, err := Build(demoDefs())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(tree["terraform/main.tf.json"], &doc); err != nil {
		t.Fatal(err)
	}
	tf, ok := doc["terraform"].(map[string]any)
	if !ok {
		t.Fatalf("no terraform block in stack: %v", doc)
	}
	rp, _ := tf["required_providers"].(map[string]any)
	aws, _ := rp["aws"].(map[string]any)
	if aws["source"] != "hashicorp/aws" || aws["version"] != "~> 6.0" {
		t.Errorf("aws provider pin = %v, want source hashicorp/aws version ~> 6.0", aws)
	}
}

func keys(tr Tree) []string {
	var k []string
	for key := range tr {
		k = append(k, key)
	}
	return k
}

func TestBuildIsDeterministic(t *testing.T) {
	a, err := Build(demoDefs())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ { // catch map-iteration nondeterminism
		b, err := Build(demoDefs())
		if err != nil {
			t.Fatal(err)
		}
		if len(a) != len(b) {
			t.Fatalf("tree size changed between runs")
		}
		for path, content := range a {
			if !bytes.Equal(content, b[path]) {
				t.Fatalf("%s: content differs between identical runs", path)
			}
		}
	}
}
