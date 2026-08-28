// Package adr_test is the mechanical half of the review discipline in
// README.md.
//
// The records are living documents: they are meant to be edited when
// understanding improves, and a record whose Last reviewed date is far behind
// the code is worse than no record at all. Nothing here can tell whether a
// record is still TRUE — only a person reading it can. What it can do is stop
// the failures that are purely mechanical: a header that does not parse, a
// status the index disagrees with, a review date that is impossible.
//
// It lives beside the records rather than in a tools directory so that the
// check is discoverable by whoever is editing them, and so `go test ./...`
// already runs it with no CI wiring of its own.
package adr_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// dateLayout is the one date format the records use. Decided may carry prose
// around its date ("2026-03 (in `thinking-core`), reconfirmed 2026-08-21");
// Last reviewed may not, because it is the field this file exists to trust.
const dateLayout = "2006-01-02"

// statuses and confidences are the vocabularies README.md documents. Keeping
// them here means a new value has to be added deliberately, in the change that
// introduces it, rather than arriving as a typo.
var (
	statuses    = map[string]bool{"Exploring": true, "Working": true, "Revisiting": true, "Dropped": true}
	confidences = map[string]bool{"High": true, "Medium": true, "Low": true}
)

var (
	recordName = regexp.MustCompile(`^(\d{4})-[a-z0-9-]+\.md$`)
	headerLine = regexp.MustCompile(`(?m)^- \*\*([A-Za-z ]+):\*\* +(.+?)\s*$`)
	anyDate    = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
	// indexRow matches a row of README.md's index table:
	// | [0004](0004-code-as-action.md) | Title | Working | High |
	indexRow = regexp.MustCompile(`^\| \[(\d{4})\]\(([^)]+)\) \| (.+?) \| (.+?) \| (.+?) \|$`)
	// mdLink finds a relative link to a sibling record.
	mdLink = regexp.MustCompile(`\]\((\d{4}-[a-z0-9-]+\.md)\)`)
)

type record struct {
	file   string
	num    string
	header map[string]string
}

// load reads every record, skipping the template: 0000 is a form to copy, and
// its header holds the vocabulary lists rather than a choice from them.
func load(t *testing.T) []record {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var out []record
	for _, e := range entries {
		m := recordName.FindStringSubmatch(e.Name())
		if m == nil || m[1] == "0000" {
			continue
		}
		b, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		hdr := map[string]string{}
		for _, hm := range headerLine.FindAllStringSubmatch(string(b), -1) {
			hdr[hm[1]] = hm[2]
		}
		out = append(out, record{file: e.Name(), num: m[1], header: hdr})
	}
	if len(out) == 0 {
		t.Fatal("no records found — is this test running outside docs/adr?")
	}
	return out
}

func TestHeadersParse(t *testing.T) {
	for _, r := range load(t) {
		for _, field := range []string{"Status", "Confidence", "Decided", "Last reviewed", "Consumers"} {
			if strings.TrimSpace(r.header[field]) == "" {
				t.Errorf("%s: missing header field %q — copy 0000-template.md", r.file, field)
			}
		}
	}
}

func TestStatusAndConfidenceAreVocabulary(t *testing.T) {
	for _, r := range load(t) {
		// "Replaced by [ADR-NNNN](…)" is the one multi-word status: it has to
		// name its successor, or the reasoning trail dead-ends.
		if s := r.header["Status"]; !statuses[s] && !strings.HasPrefix(s, "Replaced by ") {
			t.Errorf("%s: Status %q is not one word from the README vocabulary "+
				"(Exploring/Working/Revisiting/Dropped) or 'Replaced by [ADR-NNNN](…)'. "+
				"What shipped belongs in Revisions, not here.", r.file, s)
		}
		// Confidence may carry a reason after an em dash; the first word is the
		// value.
		c, _, _ := strings.Cut(r.header["Confidence"], " — ")
		if !confidences[strings.TrimSpace(c)] {
			t.Errorf("%s: Confidence %q is not High, Medium or Low", r.file, r.header["Confidence"])
		}
	}
}

func TestReviewDatesAreSane(t *testing.T) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for _, r := range load(t) {
		raw := r.header["Last reviewed"]
		reviewed, err := time.Parse(dateLayout, raw)
		if err != nil {
			t.Errorf("%s: Last reviewed %q is not a bare YYYY-MM-DD date. This is the "+
				"one field that must stay machine-readable; nuance goes in Revisions.", r.file, raw)
			continue
		}
		if reviewed.After(today) {
			t.Errorf("%s: Last reviewed %s is in the future", r.file, raw)
		}
		// A record cannot have been reviewed before the decision it records. The
		// earliest date anywhere in Decided is the lower bound, since Decided is
		// allowed prose and may name several.
		if dates := anyDate.FindAllString(r.header["Decided"], -1); len(dates) > 0 {
			earliest := dates[0]
			for _, c := range dates[1:] {
				if c < earliest {
					earliest = c
				}
			}
			if decided, err := time.Parse(dateLayout, earliest); err == nil && reviewed.Before(decided) {
				t.Errorf("%s: Last reviewed %s precedes Decided %s", r.file, raw, earliest)
			}
		}
	}
}

// TestIndexMatchesRecords is the drift catcher. README.md's index repeats each
// record's Status and Confidence, and a hand-maintained copy of a value is a
// copy that goes stale — the exact failure these records warn about everywhere
// else.
func TestIndexMatchesRecords(t *testing.T) {
	records := load(t)
	byNum := map[string]record{}
	for _, r := range records {
		byNum[r.num] = r
	}

	b, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	indexed := map[string]bool{}
	for line := range strings.SplitSeq(string(b), "\n") {
		m := indexRow.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		num, target, status, confidence := m[1], m[2], strings.TrimSpace(m[4]), strings.TrimSpace(m[5])
		indexed[num] = true

		r, ok := byNum[num]
		if !ok {
			t.Errorf("README index lists ADR-%s, but no such record exists", num)
			continue
		}
		if target != r.file {
			t.Errorf("README index links ADR-%s to %s, but the file is %s", num, target, r.file)
		}
		if status != r.header["Status"] {
			t.Errorf("README index says ADR-%s is %q; the record says %q", num, status, r.header["Status"])
		}
		if c, _, _ := strings.Cut(r.header["Confidence"], " — "); confidence != strings.TrimSpace(c) {
			t.Errorf("README index says ADR-%s is %q confidence; the record says %q", num, confidence, c)
		}
	}
	for _, r := range records {
		if !indexed[r.num] {
			t.Errorf("%s is not in README.md's index — a record nobody can find has failed at its only job", r.file)
		}
	}
}

func TestInternalLinksResolve(t *testing.T) {
	files := []string{"README.md"}
	for _, r := range load(t) {
		files = append(files, r.file)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range mdLink.FindAllStringSubmatch(string(b), -1) {
			if _, err := os.Stat(m[1]); err != nil {
				t.Errorf("%s links to %s, which does not exist", f, m[1])
			}
		}
	}
}

// reviewHorizon is how long a record may go unreviewed before this test says
// so. It is deliberately NOT a failure: staleness is a judgement about content,
// and a date-based hard failure would break pull requests that never touched
// docs/ — which is how a required check trains people to override it (see the
// note at the top of .github/workflows/ci.yml). The adr workflow reports the
// same list into the run summary, where it is visible without blocking.
const reviewHorizon = 90 * 24 * time.Hour

func TestReviewHorizon(t *testing.T) {
	cutoff := time.Now().UTC().Add(-reviewHorizon)
	for _, r := range load(t) {
		reviewed, err := time.Parse(dateLayout, r.header["Last reviewed"])
		if err != nil {
			continue // TestReviewDatesAreSane owns this failure.
		}
		if reviewed.Before(cutoff) {
			t.Logf("%s was last reviewed %s (over %d days ago) — read it when you next "+
				"touch its area: bump the date if it still reads true, fix it if not",
				r.file, r.header["Last reviewed"], int(reviewHorizon.Hours()/24))
		}
	}
}
