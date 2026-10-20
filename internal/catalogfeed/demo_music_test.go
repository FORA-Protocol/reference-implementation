package catalogfeed

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Every playable music track must expose all three editorially distinct
// alternatives without entitlement scopes hiding any of them from the agent.
func TestDemoMusicLicenseAlternatives(t *testing.T) {
	t.Parallel()
	tracks := demoMusicTracks(t)
	if len(tracks) != demoMusicTrackCount {
		t.Fatalf("playable music tracks = %d, want %d", len(tracks), demoMusicTrackCount)
	}
	for _, track := range tracks {
		t.Run(track.Path, func(t *testing.T) {
			t.Parallel()
			requireMusicAlternatives(t, track)
			requireMusicBytes(t, track)
			requireMusicMetadata(t, track)
		})
	}
}

// demoMusicTrackCount is the number of playable MP3 tracks in music.jsonl.
const demoMusicTrackCount = 10

func requireMusicAlternatives(t *testing.T, track Record) {
	t.Helper()
	if len(track.Terms) != 3 {
		t.Fatalf("terms = %d, want three license alternatives", len(track.Terms))
	}
	wants := []struct {
		name, rate                   string
		functions, users, prohibited []string
	}{
		{"Online Creator", "29", []string{"sync", "stream"}, []string{"individual"}, []string{"ai-train", "advertising", "broadcast"}},
		{"Broadcast", "450", []string{"sync", "broadcast", "stream"}, []string{"broadcaster", "commercial_entity"}, []string{"ai-train", "advertising"}},
		{"Advertising", "1200", []string{"sync", "advertising", "broadcast", "stream"}, []string{"commercial_entity"}, []string{"ai-train", "editorial"}},
	}
	for i, want := range wants {
		term := track.Terms[i]
		if term.Semantics != "reference_only" || len(term.Scopes) != 0 {
			t.Errorf("%s must reference its document without hiding behind scopes", want.name)
		}
		if term.Pricing == nil || *term.Pricing != (Pricing{Model: "flat", Rate: want.rate, Currency: "EUR", Metering: "none"}) {
			t.Errorf("%s pricing = %+v, want flat EUR %s with metering none", want.name, term.Pricing, want.rate)
		}
		if !slices.Equal(term.Functions, want.functions) || !slices.Equal(term.UserTypes, want.users) ||
			!slices.Equal(term.ProhibitedFunctions, want.prohibited) || !slices.Equal(term.Geos, []string{"*"}) {
			t.Errorf("%s restrictions do not match its use and customer", want.name)
		}
		if len(term.Quotas) != 1 || term.Quotas[0] != (Quota{Metric: "vendor:productions", Limit: 1, Window: "total"}) {
			t.Errorf("%s must cover one production", want.name)
		}
		if len(term.Obligations) == 0 || !strings.Contains(term.Obligations[0].Detail, want.name) {
			t.Errorf("%s must identify its governing document section", want.name)
		}
	}
}

// demoMusicTracks returns the playable MP3 records of the demo music feed.
func demoMusicTracks(t *testing.T) []Record {
	t.Helper()
	var tracks []Record
	for _, record := range parseDemoFeed(t, "music.jsonl") {
		if strings.HasSuffix(record.Path, ".mp3") {
			tracks = append(tracks, record)
		}
	}
	return tracks
}

func requireMusicBytes(t *testing.T, track Record) {
	t.Helper()
	root := "../../deploy/content/demo/harmonia-records"
	audio, err := os.ReadFile(filepath.Join(root, track.Path))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("sha256:%x", sha256.Sum256(audio)); got != track.ContentHash {
		t.Fatalf("audio digest = %s, feed = %s", got, track.ContentHash)
	}
	if track.License == nil || !strings.HasSuffix(track.License.URI, "/licensing/harmonia/music-v0.1.txt") {
		t.Fatal("music must reference the served combined license document")
	}
	doc, err := os.ReadFile(filepath.Join(root, "licensing/harmonia/music-v0.1.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("sha256:%x", sha256.Sum256(doc)); got != track.License.URIDigest {
		t.Fatalf("license digest = %s, feed = %s", got, track.License.URIDigest)
	}
	for _, section := range []string{"ONLINE CREATOR", "BROADCAST", "ADVERTISING"} {
		if !strings.Contains(string(doc), "HARMONIA RECORDS — "+section) {
			t.Errorf("combined document missing %s", section)
		}
	}
}

// requireMusicMetadata checks the track carries every field the Broker's
// catalog search reads. A missing field fails an exclusion or a range, so the
// track would silently drop out of results such as "no vocals".
func requireMusicMetadata(t *testing.T, track Record) {
	t.Helper()
	var m struct {
		Genre       string   `json:"music.genre"`
		Mood        []string `json:"music.mood"`
		BPM         int      `json:"music.bpm"`
		Energy      string   `json:"music.energy"`
		Vocals      *bool    `json:"music.vocals"`
		Instruments []string `json:"music.instruments"`
		Duration    float64  `json:"music.duration_seconds"`
		SuitableFor []string `json:"music.suitable_for"`
	}
	if err := json.Unmarshal(track.Ext, &m); err != nil {
		t.Fatal(err)
	}
	if m.Genre == "" || len(m.Mood) == 0 || m.BPM <= 0 || m.Vocals == nil ||
		len(m.Instruments) == 0 || m.Duration <= 0 || len(m.SuitableFor) == 0 {
		t.Errorf("incomplete music metadata: %+v", m)
	}
	if !slices.Contains([]string{"low", "medium", "high"}, m.Energy) {
		t.Errorf("energy = %q, want low, medium or high", m.Energy)
	}
}

// The demo page lists every playable track in a table row with its credit,
// its provenance file and one cell per field the catalog search reads, so a
// person can check a search result against the page. The page is written by
// hand; this keeps it equal to the feed.
func TestDemoPageListsEveryTrackWithItsTags(t *testing.T) {
	t.Parallel()
	page, err := os.ReadFile("../../deploy/content/demo/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, track := range demoMusicTracks(t) {
		t.Run(track.Path, func(t *testing.T) {
			t.Parallel()
			var m struct {
				Artist      string   `json:"music.artist"`
				ISRC        string   `json:"music.isrc"`
				Genre       string   `json:"music.genre"`
				Mood        []string `json:"music.mood"`
				BPM         int      `json:"music.bpm"`
				Energy      string   `json:"music.energy"`
				Vocals      bool     `json:"music.vocals"`
				Instruments []string `json:"music.instruments"`
				SuitableFor []string `json:"music.suitable_for"`
				Duration    float64  `json:"music.duration_seconds"`
			}
			if err := json.Unmarshal(track.Ext, &m); err != nil {
				t.Fatal(err)
			}
			cells := fmt.Sprintf("<td>%s</td><td>%s</td><td>%d</td><td>%s</td><td>%t</td><td>%s</td><td>%s</td><td>%g</td>",
				m.Genre, strings.Join(m.Mood, ", "), m.BPM, m.Energy, m.Vocals,
				strings.Join(m.SuitableFor, ", "), strings.Join(m.Instruments, ", "), m.Duration)
			line := requirePageLine(t, string(page), track.Path)
			title, _, _ := strings.Cut(track.Title, " — ")
			credit := `">` + title + `</a><br><small>` + m.Artist + ` (incompetech.com), CC BY 4.0`
			if !strings.Contains(line, credit) {
				t.Errorf("page line %q does not carry the credit %q", line, credit)
			}
			if !strings.Contains(line, "</small></td>"+cells+"</tr>") {
				t.Errorf("page row %q does not show the search fields %q", line, cells)
			}
			requireProvenanceLink(t, line, m.ISRC)
		})
	}
}

// requirePageLine returns the one table row that links the track.
func requirePageLine(t *testing.T, page, path string) string {
	t.Helper()
	var found []string
	for _, line := range strings.Split(page, "\n") {
		if strings.Contains(line, `<a href="`+path+`">`) {
			found = append(found, line)
		}
	}
	if len(found) != 1 {
		t.Fatalf("page links %s %d times, want once", path, len(found))
	}
	return found[0]
}

// requireProvenanceLink checks the line links a provenance file that exists
// and belongs to the track.
func requireProvenanceLink(t *testing.T, line, isrc string) {
	t.Helper()
	const prefix, suffix = `<a href="/tracks/`, `-provenance.txt">source and changes</a>`
	end := strings.Index(line, suffix)
	if end < 0 {
		t.Fatal("page line has no provenance link")
	}
	start := strings.LastIndex(line[:end], prefix) + len(prefix)
	name := line[start:end] + "-provenance.txt"
	doc, err := os.ReadFile(filepath.Join("../../deploy/content/demo/harmonia-records/tracks", name))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "ISRC: "+isrc+"\n") {
		t.Errorf("%s does not name ISRC %s", name, isrc)
	}
}
