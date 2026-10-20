// Structural guard: the local note of where an agent registered goes when the
// agent's own account goes.
//
// The third rule over the note table, and its own file for the reason the other
// two are split: they are independent rules, and the linter caps a file at 500
// lines whatever it holds. The sibling files ask what the table may HOLD and what
// this service may LOG. This one asks how long a row LIVES.
//
// It reads migrations, like the column rule, but it cannot share that rule's
// shape. A column set is the union of what every migration leaves behind. A
// retention promise is the LAST thing said about one foreign key -- a later
// migration may drop the reference and add it back with a different ON DELETE
// action, and the promise is then whatever that last statement said.
//
// Like its siblings, this breaks by addition rather than by edit. Nobody deletes
// the promise; somebody re-creates the constraint while adding an index or
// renaming a column, writes ON DELETE RESTRICT because that is the safer-looking
// word, and inverts the guarantee -- the notes now BLOCK the developer's deletion
// instead of going with it, and the migration's retention paragraph stays there
// being false.
package guards

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// referenceClause matches a foreign key from the note table to the developer
// account, capturing the ON DELETE action when one is written.
//
// The action is OPTIONAL in the pattern although the promise requires CASCADE,
// because a reference written without an action is the case worth catching
// loudest: Postgres defaults it to NO ACTION, so omitting the clause silently
// inverts the promise exactly as RESTRICT does. Requiring CASCADE in the pattern
// would make such a statement simply not match, and a statement that does not
// match is a statement this guard does not count.
//
// The schema qualifier is optional for the reason it is optional on the ALTER
// scan in the sibling file: search_path makes the bare name ordinary SQL.
var referenceClause = regexp.MustCompile(
	`(?is)REFERENCES\s+(?:identity\.)?developer_account\s*\(\s*subdomain\s*\)` +
		`(?:\s+ON\s+DELETE\s+(CASCADE|RESTRICT|NO\s+ACTION|SET\s+NULL|SET\s+DEFAULT))?`,
)

// dropConstraintClause matches a constraint being removed from the note table.
//
// It does not check WHICH constraint. Naming one would tie this guard to an
// identifier the creating migration never wrote -- the reference there is inline
// and unnamed, so its real name is one Postgres generated -- and a guard that
// matches nothing is worse than one that occasionally asks a question twice.
// Dropping any constraint on this table and not restating the reference leaves
// the retention promise unproven, which is the answer this returns.
var dropConstraintClause = regexp.MustCompile(`(?is)\bDROP\s+CONSTRAINT\b`)

// retentionAction returns the ON DELETE action in force on the note table's
// reference to the developer account after every migration has run, or "" when
// no reference is left standing.
//
// Events are applied in the order they are WRITTEN, across files in migration
// order and within a file by position, because that is the order Postgres applies
// them: a DROP followed by an ADD in one statement list is a replacement, and the
// same two in the other order is a removal.
func retentionAction(t *testing.T, migrations []string) string {
	t.Helper()
	action := ""
	for _, migration := range migrations {
		for _, ev := range retentionEvents(migration) {
			action = ev.action
		}
	}
	return action
}

// retentionEvent is one statement that establishes or removes the reference.
// action is the ON DELETE action it leaves in force, or "" for a removal and for
// a reference that names no action.
type retentionEvent struct {
	at     int
	action string
}

// retentionEvents finds every such statement in one migration, in written order.
//
// Only two places are read, and deliberately: the note table's own CREATE body,
// and ALTER statements against the note table. A REFERENCES clause anywhere else
// in the file belongs to another table and says nothing about this promise.
func retentionEvents(migration string) []retentionEvent {
	var events []retentionEvent
	if body := noteTableBody.FindStringSubmatchIndex(migration); body != nil {
		events = append(events, eventsIn(migration[body[2]:body[3]], body[2])...)
	}
	for _, alter := range alterStatement.FindAllStringSubmatchIndex(migration, -1) {
		events = append(events, eventsIn(migration[alter[2]:alter[3]], alter[2])...)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].at < events[j].at })
	return events
}

// eventsIn reads one statement body, offset by base so events from different
// statements sort against each other.
func eventsIn(body string, base int) []retentionEvent {
	refs := referenceClause.FindAllStringSubmatchIndex(body, -1)
	drops := dropConstraintClause.FindAllStringIndex(body, -1)
	events := make([]retentionEvent, 0, len(refs)+len(drops))
	for _, m := range refs {
		action := ""
		if m[2] >= 0 {
			action = normaliseAction(body[m[2]:m[3]])
		}
		events = append(events, retentionEvent{at: base + m[0], action: action})
	}
	for _, m := range drops {
		events = append(events, retentionEvent{at: base + m[0], action: ""})
	}
	return events
}

// normaliseAction folds case and inner whitespace so "on delete  no   action"
// and "NO ACTION" compare equal.
func normaliseAction(raw string) string {
	return strings.ToUpper(strings.Join(strings.Fields(raw), " "))
}

// TestRegistrationNoteGoesWithTheAgent fails when the note's lifetime stops
// being structural.
func TestRegistrationNoteGoesWithTheAgent(t *testing.T) {
	t.Parallel()
	if got := retentionAction(t, allNoteMigrations(t)); got != "CASCADE" {
		t.Errorf("the migrations leave the note's reference to the developer account at %q, "+
			"want CASCADE — without it the migration's retention paragraph is a promise the "+
			"schema does not keep, and the notes block a developer's deletion instead of "+
			"going with it", got)
	}
}

// TestMeta_RetentionDetectorFollowsALaterMigration drives the detector to failure
// over tables it writes itself. Without these rows nothing had ever shown it
// fail: the behavioural tests cannot reach it, because there is no
// agent-deletion flow to drive and a test that issued its own DELETE would be
// arranging state by reaching past every layer.
func TestMeta_RetentionDetectorFollowsALaterMigration(t *testing.T) {
	t.Parallel()
	const replaceFK = "ALTER TABLE identity.exchange_registration DROP CONSTRAINT some_fkey;\n" +
		"ALTER TABLE identity.exchange_registration ADD CONSTRAINT some_fkey " +
		"FOREIGN KEY (subdomain) REFERENCES identity.developer_account (subdomain) ON DELETE %s;\n"
	for name, tc := range map[string]struct {
		migrations []string
		want       string
	}{
		"the creating migration alone": {
			migrations: []string{noteTableWith("")},
			want:       "CASCADE",
		},
		"a later migration inverting the promise": {
			migrations: []string{noteTableWith(""), sprintfFK(replaceFK, "RESTRICT")},
			want:       "RESTRICT",
		},
		"a later migration restating the promise": {
			migrations: []string{noteTableWith(""), sprintfFK(replaceFK, "CASCADE")},
			want:       "CASCADE",
		},
		"a reference re-added with no action at all": {
			migrations: []string{
				noteTableWith(""),
				"ALTER TABLE identity.exchange_registration DROP CONSTRAINT some_fkey;\n" +
					"ALTER TABLE identity.exchange_registration ADD CONSTRAINT some_fkey " +
					"FOREIGN KEY (subdomain) REFERENCES identity.developer_account (subdomain);\n",
			},
			want: "",
		},
		"the reference dropped and never restored": {
			migrations: []string{
				noteTableWith(""),
				"ALTER TABLE identity.exchange_registration DROP CONSTRAINT some_fkey;\n",
			},
			want: "",
		},
		"an unqualified later migration": {
			migrations: []string{
				noteTableWith(""),
				"SET search_path TO identity;\n" +
					"ALTER TABLE exchange_registration DROP CONSTRAINT some_fkey;\n" +
					"ALTER TABLE exchange_registration ADD CONSTRAINT some_fkey " +
					"FOREIGN KEY (subdomain) REFERENCES developer_account (subdomain) ON DELETE RESTRICT;\n",
			},
			want: "RESTRICT",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := retentionAction(t, tc.migrations); got != tc.want {
				t.Errorf("detector read %q from %v, want %q — a migration written that way "+
					"changes the note's lifetime and this guard would not see it",
					got, tc.migrations, tc.want)
			}
		})
	}
}

// sprintfFK fills the ON DELETE action into the replacement template. Written as
// a helper so the rows above differ by one word and nothing else.
func sprintfFK(template, action string) string {
	return strings.Replace(template, "%s", action, 1)
}
