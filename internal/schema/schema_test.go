package schema

import "testing"

func TestRegistryContainsConfigAndS3(t *testing.T) {
	for _, kind := range []string{"config", "s3-bucket"} {
		k, ok := Lookup(kind)
		if !ok {
			t.Fatalf("Lookup(%q): not found", kind)
		}
		if k.Description == "" {
			t.Errorf("%s: empty kind description", kind)
		}
		for _, f := range k.Fields {
			if f.Description == "" {
				t.Errorf("%s.%s: empty field description (a kind without complete field docs is unfinished — DD-13)", kind, f.Name)
			}
		}
	}
}

func TestS3BucketFields(t *testing.T) {
	k, _ := Lookup("s3-bucket")
	name, ok := k.Field("name")
	if !ok || !name.Required {
		t.Errorf("s3-bucket.name: want required field, got ok=%v required=%v", ok, name.Required)
	}
	desc, ok := k.Field("description")
	if !ok || !desc.Required {
		t.Errorf("s3-bucket.description: want required field, got ok=%v required=%v", ok, desc.Required)
	}
	if _, ok := k.Field("nope"); ok {
		t.Error("Field(nope): want not found")
	}
}

func TestLookupUnknownKind(t *testing.T) {
	if _, ok := Lookup("volcano"); ok {
		t.Error("Lookup(volcano): want not found")
	}
}
