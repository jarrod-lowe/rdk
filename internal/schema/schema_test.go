package schema

import "testing"

func TestKindField(t *testing.T) {
	k := Kind{
		Name:        "example",
		Description: "An example kind.",
		Fields: []Field{
			{Name: "name", Type: StringType, Required: true,
				Description: "The name.", Example: "demo"},
		},
	}
	f, ok := k.Field("name")
	if !ok {
		t.Fatal(`Field("name"): not found`)
	}
	if !f.Required || f.Type != StringType {
		t.Errorf("Field(name) = %+v, want required string", f)
	}
	if _, ok := k.Field("nope"); ok {
		t.Error("Field(nope): want not found")
	}
}
