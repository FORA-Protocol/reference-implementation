// Structural guard: the local note of where an agent registered has no column
// that could hold what was submitted.
//
// The sibling file carries the other half of the same promise -- that this
// service never writes the submitted registration details to a log line. They
// are split because they are two independent rules over two different inputs,
// one reading Go source and one reading a migration, and because the linter caps
// a file at 500 lines whatever it holds.
//
// Like its sibling, this rule breaks by addition rather than by edit. Nobody
// deletes the promise; somebody adds a payload column while making the hint list
// more useful, and the sentence in the migration that creates the table stays
// there being false.
package guards

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// registrationNoteMigrations is every migration that can shape the note table,
// and the column set they leave behind IS the promise: nothing here can hold what
// was submitted because there is nowhere to put it.
//
// The whole directory rather than the one file that creates the table. A column
// does not have to arrive in a CREATE -- this repository already adds and drops
// them with ALTER TABLE in a later migration -- so watching only the creating
// file watches one of two doors. Nothing else is behind the second: the
// repository selects columns by name, so a column that is written and never
// selected is invisible to the behavioural tests.
const registrationNoteMigrations = "src/identity/internal/db/migrations"

// allNoteMigrations reads every migration that can shape the note table, in the
// order they run.
//
// The whole directory, where the check this replaces read only the file that
// creates the table. That file's reasoning was half right: a cascade IS a
// property of the reference the CREATE declares, and finding the clause loose
// somewhere in the directory would not say it applies to this table. What it
// missed is that a later migration can drop that reference and declare a new one.
// Reading one file answers "was it ever promised"; the promise is whether it
// still holds. The column guard reads the same set for the same reason, which is
// why this is shared rather than written out twice.
func allNoteMigrations(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(repoRoot(t), registrationNoteMigrations, "*.up.sql"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no migrations under %s (err=%v)", registrationNoteMigrations, err)
	}
	slices.Sort(paths)
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		raw, readErr := os.ReadFile(path) //nolint:gosec // a path this test just globbed
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		out = append(out, string(raw))
	}
	return out
}

// noteColumns are the three the table may have. Written out rather than derived,
// so ADDING one fails here and has to be argued for in the diff.
var noteColumns = []string{"subdomain", "exchange", "registered_at"}

// noteTableBody captures what is between the note table's parentheses, so a
// second table added to this migration cannot be read as part of the first.
var noteTableBody = regexp.MustCompile(`(?s)CREATE TABLE identity\.exchange_registration \((.*?)\n\);`)

// columnLine matches a column definition inside that body: a name at the start of
// a four-space-indented line, followed by anything at all.
//
// It knows NOTHING about types, and that is deliberate. It used to carry an
// alternation of the seven this table happens to use, so a column of any other
// type was invisible — "payload json" or "details varchar(2000)" left the guard
// reporting exactly the three columns it expected. The whole point of the check
// is that an extra column is a place to put the payload, and that property did
// not hold outside the list. Writing the alternation case-insensitively would
// only have moved the blind spot; the fix is to stop reading the type. The
// meta-test below found the second hole before this comment was written: the
// first replacement still missed an upper-cased type.
//
// It knows nothing about indentation either. Indentation is not part of the
// language, so a column declared at the start of a line is a column, and the
// anchor accepts none. A quoted identifier is one too: the quotes sit outside
// the capture, so the guard reads the name whether or not it was written with
// them. That is deliberately conservative -- Postgres does NOT fold a quoted
// name, so "Payload" and payload are two columns to it and one to this guard.
// Either way an extra column fails, which is the only question being asked.
//
// Continuation lines and table constraints are excluded by NAMING what opens
// them: bodyKeywords below carries the list. That is what the structure used to
// do, badly, through the indent.
var columnLine = regexp.MustCompile(`(?m)^[ \t]*"?([A-Za-z_][A-Za-z0-9_$]*)"?[ \t]+\S`)

// alterStatement captures the body of one ALTER TABLE against the note table:
// everything after the table name up to the statement's semicolon. The table is
// named explicitly, because another table's ALTER in the same file says nothing
// about this promise.
//
// ONLY is optional after TABLE. The grammar admits it and pg_dump emits it as a
// matter of course, so a pattern requiring the schema name directly after TABLE
// reads a routine dump as declaring nothing.
//
// The SCHEMA QUALIFIER is optional too, and that one is the difference between a
// guard and a suggestion. "SET search_path TO identity;" followed by a bare
// "ALTER TABLE exchange_registration ADD COLUMN payload jsonb;" is ordinary legal
// SQL that adds the exact column this file forbids, and a pattern demanding
// "identity." never sees it. Accepting the bare name can only OVER-match — an
// unrelated table of the same name under another schema would be reported here —
// and over-matching fails safe, which is the trade this file already takes for
// quoted identifiers.
//
// The CREATE pattern above deliberately does not need the same widening, and the
// asymmetry is worth naming so nobody 'fixes' it. An unqualified CREATE makes
// noteColumnsAcross find no table at all, which is a hard failure rather than a
// quiet pass: that direction fails closed. Only the ALTER scan could fail open,
// because a statement it does not match is simply a change it does not count.
//
// The body is captured whole and scanned separately below, because ONE statement
// may carry ANY NUMBER of clauses and a single regex cannot see past the first.
// Non-overlapping matching resumes after the first column name, where there is
// no second ALTER TABLE left to anchor on -- so a two-column change reported one
// column and hid the other. That is the shape this repository writes multi-column
// changes in, which made the guard blindest to its own house style.
var alterStatement = regexp.MustCompile(
	`(?is)ALTER\s+TABLE\s+(?:ONLY\s+)?(?:identity\.)?exchange_registration\b([^;]*);`,
)

// columnOp matches one clause inside that body: what it does, and to which name.
//
// COLUMN is optional after ADD -- PostgreSQL's grammar is
// "ADD [ COLUMN ] [ IF NOT EXISTS ] column_name data_type" -- so requiring it
// let a column in under the shorter spelling. DROP and RENAME are read for the
// same reason the guard reads ADD at all: a table the guard describes with three
// columns is not that table any more once one of them goes, and a rename is a
// removal and an addition written as one clause.
var columnOp = regexp.MustCompile(
	`(?is)\b(ADD|DROP|RENAME)\s+(?:COLUMN\s+)?(?:IF\s+(?:NOT\s+)?EXISTS\s+)?` +
		`"?([A-Za-z_][A-Za-z0-9_$]*)"?(?:\s+TO\s+"?([A-Za-z_][A-Za-z0-9_$]*)"?)?`,
)

// bodyKeywords open a line that continues the PREVIOUS column's definition, or
// declares something that is not a column at all.
//
// The indent used to do this job -- exactly four spaces meant "a column", more
// meant "a continuation". That is not a rule Postgres has, and enforcing it made
// the detector blind to any column laid out differently. Matching the keyword
// instead reads what the line IS rather than how far it was pushed.
//
// The ALTER scan reads the same list, and gets the same thing from it: an
// ADD CONSTRAINT, an ADD PRIMARY KEY or a DROP CONSTRAINT names a keyword where
// a column name would be, and is skipped rather than counted as a column called
// "constraint".
var bodyKeywords = map[string]bool{
	"references": true, "constraint": true, "primary": true, "foreign": true,
	"unique": true, "check": true, "on": true, "not": true, "default": true,
}

// TestRegistrationNoteHoldsNoPayloadColumns fails when the note's table grows a
// column beyond the three it is allowed.
//
// Every migration that could shape the table has to be read, because either one
// can add a column: the CREATE that makes it, or a later ALTER. Both directions
// fail: an extra column is a place to put the payload, and a missing one means
// the guard is reading a table that is no longer the one it describes.
func TestRegistrationNoteHoldsNoPayloadColumns(t *testing.T) {
	t.Parallel()
	got := noteColumnsAcross(t, allNoteMigrations(t))
	if strings.Join(got, ",") != strings.Join(noteColumns, ",") {
		t.Errorf("the migrations leave the note table with columns %v, want exactly %v — "+
			"the note records THAT a registration happened and nothing about what was "+
			"submitted, so any further column is somewhere the payload could land",
			got, noteColumns)
	}
}

// noteColumnsIn returns the column names the note table declares, in order.
//
// Split out from the test so the meta-tests below can drive the detector over a
// table they write themselves. Without that, nothing had ever shown this
// detector fail: the behavioural test cannot backstop it either, because the
// repository selects columns by name, so a written-but-never-selected column is
// invisible to it.
func noteColumnsIn(t *testing.T, migration string) []string {
	t.Helper()
	body := noteTableBody.FindStringSubmatch(migration)
	if body == nil {
		return nil
	}
	matches := columnLine.FindAllStringSubmatch(body[1], -1)
	got := make([]string, 0, len(matches))
	for _, m := range matches {
		// Folded, because Postgres folds an unquoted identifier: `Payload json`
		// declares a column named payload, and comparing the spelling as written
		// would let one through under a capital.
		name := strings.ToLower(m[1])
		if bodyKeywords[name] {
			continue
		}
		got = append(got, name)
	}
	return got
}

// TestMeta_ColumnDetectorSeesAnyType is the detector's own negative. The sibling
// guard's payload-log detector has carried one from the start; this one did not.
//
// Every case is a fourth column the table must not grow. The types are chosen to
// be outside the list the detector used to carry, because that list was the
// defect: a column it could not name did not exist as far as the guard was
// concerned.
func TestMeta_ColumnDetectorSeesAnyType(t *testing.T) {
	t.Parallel()
	for name, decl := range map[string]string{
		"json":                "    payload       json NOT NULL",
		"varchar with size":   "    details       varchar(2000) NOT NULL",
		"numeric":             "    amount        numeric(12,2) NOT NULL",
		"an array":            "    tags          text[] NOT NULL",
		"a domain type":       "    contact       email_address NOT NULL",
		"an upper-cased type": "    fields        JSONB NOT NULL",
		"no type at all":      "    fields        text",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := noteColumnsIn(t, noteTableWith(decl))
			want := strings.Fields(strings.TrimSpace(decl))[0]
			if !slices.Contains(got, want) {
				t.Fatalf("the detector did not see the column %q declared by %q; it read %v",
					want, decl, got)
			}
		})
	}
}

// TestMeta_ColumnDetectorPassesTheTableWeWant is the other half: a detector that
// flagged the three allowed columns, or read a constraint as one, would be
// removed rather than obeyed.
func TestMeta_ColumnDetectorPassesTheTableWeWant(t *testing.T) {
	t.Parallel()
	got := noteColumnsIn(t, noteTableWith(""))
	if strings.Join(got, ",") != strings.Join(noteColumns, ",") {
		t.Errorf("the detector read %v from the table it is meant to accept, want %v",
			got, noteColumns)
	}
}

// noteColumnsAcross returns the columns the note table has once every migration
// has been applied, in order.
//
// A FOLD rather than a collection, and that is the whole difference between the
// two directions the guard claims to catch. Names were only ever appended, so a
// migration that dropped a column left it in the answer and the exact-set
// comparison passed against a table that no longer had it -- the direction the
// test's own doc promises and did not deliver. Applying each statement in the
// order it is written is what makes a drop subtract and a rename move.
func noteColumnsAcross(t *testing.T, migrations []string) []string {
	t.Helper()
	var got []string
	created := false
	for _, migration := range migrations {
		if cols := noteColumnsIn(t, migration); cols != nil {
			got = append(got, cols...)
			created = true
		}
		got = applyAlters(got, migration)
	}
	if !created {
		t.Fatal("no CREATE TABLE identity.exchange_registration in any migration — the " +
			"guard is reading a directory that no longer creates the table it describes")
	}
	return got
}

// applyAlters folds one migration's ALTER TABLE statements over cols, clause by
// clause, in the order they are written.
//
// Order matters twice over. Within a statement, because a rename after an add
// acts on the added column. Across statements, because the exact-set assertion
// compares the sequence and not the set -- a table whose columns are the right
// three in the wrong order is a table the guard is no longer describing.
func applyAlters(cols []string, migration string) []string {
	for _, stmt := range alterStatement.FindAllStringSubmatch(migration, -1) {
		for _, op := range columnOp.FindAllStringSubmatch(stmt[1], -1) {
			name := strings.ToLower(op[2])
			if bodyKeywords[name] {
				continue
			}
			switch strings.ToUpper(op[1]) {
			case "ADD":
				cols = append(cols, name)
			case "DROP":
				cols = slices.DeleteFunc(cols, func(c string) bool { return c == name })
			case "RENAME":
				// The TO half is what separates a column rename from a TABLE
				// rename, which names no column and must leave the list alone.
				if to := strings.ToLower(op[3]); to != "" {
					if i := slices.Index(cols, name); i >= 0 {
						cols[i] = to
					}
				}
			}
		}
	}
	return cols
}

// TestMeta_ColumnDetectorSeesEveryLegalSpelling covers the axes the type table
// above does not. Every one of its rows names the column in lower case with a
// four-space indent, so none of them can see a name or a layout the detector
// cannot read -- and a column is a place to put the payload whatever it is
// called.
//
// Postgres folds an unquoted identifier to lower case, so `Payload json` really
// does declare a column named payload. Digits are legal after the first
// character. Indentation is not part of the language at all.
func TestMeta_ColumnDetectorSeesEveryLegalSpelling(t *testing.T) {
	t.Parallel()
	for name, decl := range map[string]string{
		"an upper-cased name": "    Payload       json NOT NULL",
		"a name with a digit": "    payload2      json NOT NULL",
		"a tab indent":        "\tpayload       json NOT NULL",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := noteColumnsAcross(t, []string{noteTableWith(decl)})
			want := strings.ToLower(strings.Fields(strings.TrimSpace(decl))[0])
			if !slices.Contains(got, want) {
				t.Errorf("detector read %v from a table declaring %q, so a payload column "+
					"spelled that way is invisible to this guard", got, want)
			}
		})
	}
}

// TestMeta_ColumnDetectorSeesALaterMigration covers the other door.
//
// A column does not have to arrive in the migration that created the table. This
// repository already adds and drops columns with ALTER TABLE in a later file, so
// a guard reading only the CREATE would pass while the table carries a payload
// column added afterwards. Nothing else catches it: the repository selects
// columns by name, so one that is written and never selected is invisible to the
// behavioural tests.
func TestMeta_ColumnDetectorSeesALaterMigration(t *testing.T) {
	t.Parallel()
	later := "ALTER TABLE identity.exchange_registration ADD COLUMN payload jsonb;\n"
	got := noteColumnsAcross(t, []string{noteTableWith(""), later})
	if !slices.Contains(got, "payload") {
		t.Errorf("detector read %v across two migrations, so a column added by the "+
			"second one is invisible to this guard", got)
	}
}

// TestMeta_ColumnDetectorSeesEveryAlterSpelling covers the ALTER grammar rather
// than the layout.
//
// PostgreSQL writes "ADD [ COLUMN ] [ IF NOT EXISTS ] name type", allows ONLY
// after TABLE, and lets one statement carry any number of clauses. Every row
// here adds a payload column and is legal SQL, so a detector that reads only the
// one spelling this table happens to use elsewhere reports the expected three
// while the table holds a fourth.
//
// The multi-clause row is the one that matters most: it is how this repository
// already writes a change that touches several columns, so the layout the guard
// is blindest to is the layout its own migrations are written in.
func TestMeta_ColumnDetectorSeesEveryAlterSpelling(t *testing.T) {
	t.Parallel()
	for name, alter := range map[string]string{
		"a second clause in one statement": "ALTER TABLE identity.exchange_registration\n" +
			"    ADD COLUMN last_seen_at timestamptz,\n" +
			"    ADD COLUMN payload jsonb;\n",
		"COLUMN omitted":   "ALTER TABLE identity.exchange_registration ADD payload jsonb;\n",
		"ONLY after TABLE": "ALTER TABLE ONLY identity.exchange_registration ADD COLUMN payload jsonb;\n",
		"a renamed column": "ALTER TABLE identity.exchange_registration RENAME COLUMN exchange TO payload;\n",
		"IF NOT EXISTS and no COLUMN": "ALTER TABLE identity.exchange_registration " +
			"ADD IF NOT EXISTS payload jsonb;\n",
		// The schema left off, reached through search_path. Legal SQL, the column
		// this file exists to forbid, and invisible to a pattern that demands the
		// qualifier -- which is what this one used to be.
		"the schema qualifier omitted": "SET search_path TO identity;\n" +
			"ALTER TABLE exchange_registration ADD COLUMN payload jsonb;\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := noteColumnsAcross(t, []string{noteTableWith(""), alter})
			if !slices.Contains(got, "payload") {
				t.Errorf("detector read %v after %q, so a payload column declared that "+
					"way is invisible to this guard", got, alter)
			}
		})
	}
}

// TestMeta_ColumnDetectorFollowsARemoval is the direction the guard's own header
// promises and did not have.
//
// The header says both directions fail -- an extra column is a place to put the
// payload, and a missing one means the guard is reading a table that is no
// longer the one it describes. The second only holds if a column that goes away
// stops being counted, which needs the detector to apply a DROP rather than
// collect names. A rename is the same operation twice: the old name goes and the
// new one arrives, and a detector that only appended would report both.
func TestMeta_ColumnDetectorFollowsARemoval(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		alter string
		gone  string
	}{
		"a dropped column": {
			alter: "ALTER TABLE identity.exchange_registration DROP COLUMN registered_at;\n",
			gone:  "registered_at",
		},
		"the old name of a renamed column": {
			alter: "ALTER TABLE identity.exchange_registration RENAME COLUMN exchange TO payload;\n",
			gone:  "exchange",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := noteColumnsAcross(t, []string{noteTableWith(""), tc.alter})
			if slices.Contains(got, tc.gone) {
				t.Errorf("detector read %v after %q, so it still counts %q and would pass "+
					"against a table that no longer has it", got, tc.alter, tc.gone)
			}
		})
	}
}

// TestMeta_ColumnDetectorSeesEveryColumnLayout covers the two body layouts the
// spelling table above cannot express, because that one derives the expected
// name from the declaration's first field and both of these change what that is.
//
// Indentation is not part of the language, so a column declared at the start of
// a line is a column. A quoted identifier is one too, and it is the spelling
// that carries a name Postgres would otherwise fold.
func TestMeta_ColumnDetectorSeesEveryColumnLayout(t *testing.T) {
	t.Parallel()
	for name, decl := range map[string]string{
		"no indent at all":    "payload jsonb NOT NULL",
		"a quoted identifier": `    "payload" jsonb NOT NULL`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := noteColumnsAcross(t, []string{noteTableWith(decl)})
			if !slices.Contains(got, "payload") {
				t.Errorf("detector read %v from a table declaring %q, so a payload column "+
					"laid out that way is invisible to this guard", got, decl)
			}
		})
	}
}

// noteTableWith renders the note's CREATE TABLE with one extra line in the body,
// so a meta-test states only what it adds.
func noteTableWith(extra string) string {
	body := `CREATE TABLE identity.exchange_registration (
    subdomain     text        NOT NULL
        REFERENCES identity.developer_account (subdomain) ON DELETE CASCADE,
    exchange      text        NOT NULL,
    registered_at timestamptz NOT NULL,`
	if extra != "" {
		body += "\n" + extra + ","
	}
	return body + `
    CONSTRAINT exchange_registration_pkey PRIMARY KEY (subdomain, exchange)
);
`
}
