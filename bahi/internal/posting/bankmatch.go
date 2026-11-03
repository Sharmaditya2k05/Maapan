package posting

import (
	"sort"
	"strings"
	"time"
)

// StmtLine is a bank statement line to match. Amount is signed from the
// bank account's point of view in the books: + deposit (bank credit, books
// debit), − withdrawal.
type StmtLine struct {
	ID     string
	Date   time.Time
	Amount int64
	Text   string // description + reference + cheque no
}

// BookEntry is an unreconciled posting on the bank account: + debit, − credit.
type BookEntry struct {
	ID     string
	Date   time.Time
	Amount int64
	Refs   []string // voucher number, source ref, cheque no, UTR, bank txn reference
}

// MatchWindow is how far the bank date may be before/after the book date.
type MatchWindow struct {
	BeforeDays int // bank date earlier than book date
	AfterDays  int // bank date later than book date (cheques take days to clear)
}

// DefaultWindow: a bank may show a book entry up to 3 days earlier and 10 days later.
var DefaultWindow = MatchWindow{BeforeDays: 3, AfterDays: 10}

// Match is one line ↔ entry pairing.
type Match struct {
	LineID  string `json:"line_id"`
	EntryID string `json:"entry_id"`
	Rule    string `json:"rule"` // reference | amount_date
}

func refTokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, t := range strings.FieldsFunc(strings.ToUpper(s), func(r rune) bool {
		return !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	}) {
		out[t] = true
		if z := strings.TrimLeft(t, "0"); z != "" {
			out[z] = true
		}
	}
	return out
}

func squash(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToUpper(s) {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// refMatches: a reference token of 3+ characters containing a digit (cheque
// number, UTR, voucher serial) equal to a token of the line, leading zeros
// ignored (cheque 777 matches 000777); or a 6+ character reference found
// anywhere in the squashed line text (UTRs glued into NEFT narrations).
func refMatches(line StmtLine, refs []string) bool {
	toks := refTokens(line.Text)
	sq := squash(line.Text)
	for _, r := range refs {
		for t := range refTokens(r) {
			if len(t) >= 3 && strings.ContainsAny(t, "0123456789") && toks[t] {
				return true
			}
		}
		if n := squash(r); len(n) >= 6 && strings.ContainsAny(n, "0123456789") && strings.Contains(sq, n) {
			return true
		}
	}
	return false
}

func inWindow(l StmtLine, e BookEntry, w MatchWindow) bool {
	d := int(l.Date.Sub(e.Date).Hours() / 24)
	return d >= -w.BeforeDays && d <= w.AfterDays
}

// AutoMatch pairs statement lines with book entries one-to-one. Pass 1 takes
// same amount + date window + a matching reference (cheque no, UTR, voucher
// number) when exactly one entry qualifies. Pass 2 takes same amount + date
// window when the pairing is unique both ways (one candidate entry for the
// line, and the line is the only candidate for that entry). Anything
// ambiguous is left for manual matching.
func AutoMatch(lines []StmtLine, books []BookEntry, w MatchWindow) []Match {
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Date.Before(lines[j].Date) })
	usedL, usedE := map[string]bool{}, map[string]bool{}
	var out []Match
	cands := func(l StmtLine) []BookEntry {
		var cs []BookEntry
		for _, e := range books {
			if !usedE[e.ID] && e.Amount == l.Amount && inWindow(l, e, w) {
				cs = append(cs, e)
			}
		}
		return cs
	}
	for _, l := range lines {
		var hit []BookEntry
		for _, e := range cands(l) {
			if refMatches(l, e.Refs) {
				hit = append(hit, e)
			}
		}
		if len(hit) == 1 {
			usedL[l.ID], usedE[hit[0].ID] = true, true
			out = append(out, Match{LineID: l.ID, EntryID: hit[0].ID, Rule: "reference"})
		}
	}
	for _, l := range lines {
		if usedL[l.ID] {
			continue
		}
		cs := cands(l)
		if len(cs) != 1 {
			continue
		}
		e := cs[0]
		rivals := 0
		for _, o := range lines {
			if !usedL[o.ID] && o.Amount == e.Amount && inWindow(o, e, w) {
				rivals++
			}
		}
		if rivals != 1 {
			continue
		}
		usedL[l.ID], usedE[e.ID] = true, true
		out = append(out, Match{LineID: l.ID, EntryID: e.ID, Rule: "amount_date"})
	}
	return out
}
