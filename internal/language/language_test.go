package language

import (
	"reflect"
	"testing"
)

func TestParseOrderedNormalizesAliasesAndPreservesPriority(t *testing.T) {
	got := ParseOrdered("inglês, Français; spa → eng")
	want := []string{"en", "fr", "es"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%#v want=%#v", got, want)
	}
}

func TestCanonicalMatchesCommonEmbeddedMetadata(t *testing.T) {
	for input, want := range map[string]string{
		"eng": "en", "enm": "en", "fre": "fr", "fra": "fr", "pt-BR": "pt", "chi": "zh",
	} {
		if got := Canonical(input); got != want {
			t.Errorf("Canonical(%q)=%q want %q", input, got, want)
		}
	}
}
