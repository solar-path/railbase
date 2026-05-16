// Regression test for FEEDBACK #B10 — `railbase import data` help
// now documents the column-type cheatsheet, including the surprising
// Tags / Relations array-literal shape (`{tag1,tag2}`). The blogger
// project hit this without examples and had to guess.
package cli

import (
	"strings"
	"testing"
)

func TestImportDataHelp_ListsColumnTypeCheatsheet(t *testing.T) {
	cmd := newImportDataCmd()
	long := cmd.Long

	for _, want := range []string{
		"Column-type cheatsheet",
		"Tags / Relations",
		`"{tag1,tag2`,          // array literal example
		"text[] columns",       // explains why
		"FEEDBACK #B10",        // anchors to the issue
		"ISO-8601",             // date guidance
	} {
		if !strings.Contains(long, want) {
			t.Errorf("import-data Long help missing %q\n%s", want, long)
		}
	}
}

func TestImportDataHelp_KeepsExistingExamples(t *testing.T) {
	// Existing example invocations must still appear — regression
	// against an accidental wholesale rewrite.
	cmd := newImportDataCmd()
	long := cmd.Long
	for _, want := range []string{
		"railbase import data posts --file posts.csv",
		"railbase import data orders --file orders.csv.gz --delimiter ';'",
	} {
		if !strings.Contains(long, want) {
			t.Errorf("import-data Long help dropped existing example %q", want)
		}
	}
}
