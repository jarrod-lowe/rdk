// Package schema defines the types describing a kind and its rich field
// metadata. Every authoring aid (JSON Schema, starters, skills, error text) is
// a projection of this data (DD-13); a field without documentation is a bug.
// The kinds themselves live in internal/kind and its sub-packages.
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
