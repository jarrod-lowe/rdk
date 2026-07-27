// Package schema defines resource kinds and their rich field metadata.
// Every authoring aid (JSON Schema, starters, skills, error text) is a
// projection of this data (DD-13); a field without documentation is a bug.
package schema

// FieldType is the YAML type a field accepts.
type FieldType string

const (
	StringType FieldType = "string"
)

// Field describes one settable field of a kind.
type Field struct {
	Name        string
	Type        FieldType
	Required    bool
	Description string // shown in starters, docs, and error messages
	Example     string
}

// Kind describes one definition kind.
type Kind struct {
	Name        string
	Description string
	Fields      []Field
}

// Field returns the named field, if the kind has it.
func (k Kind) Field(name string) (Field, bool) {
	for _, f := range k.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

var registry = map[string]Kind{
	"config": {
		Name:        "config",
		Description: "Global repository configuration. Exactly one config definition is required.",
		Fields: []Field{
			{Name: "name", Type: StringType, Required: true,
				Description: "Project name; used in generated documentation and, later, naming policy.",
				Example:     "my-service"},
		},
	},
	"s3-bucket": {
		Name:        "s3-bucket",
		Description: "An S3 bucket with safe defaults (public access blocked).",
		Fields: []Field{
			{Name: "name", Type: StringType, Required: true,
				Description: "Resource name; becomes the bucket name until naming policy lands.",
				Example:     "assets"},
			{Name: "description", Type: StringType, Required: true,
				Description: "What this bucket is for; feeds generated documentation.",
				Example:     "Static assets for the public site"},
		},
	},
}

// Lookup returns the schema for a kind name.
func Lookup(kind string) (Kind, bool) {
	k, ok := registry[kind]
	return k, ok
}
