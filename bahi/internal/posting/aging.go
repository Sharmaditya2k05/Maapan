package posting

import (
	"sort"
	"time"
)

// Movement is one posting on a party ledger, signed from the party's
// "outstanding" point of view: for receivables a debit (invoice) is +, a credit
// (receipt/return) is −. For payables pass credits as + and debits as −.
type Movement struct {
	Date   time.Time
	Amount int64
	Ref    string
}

// Buckets are outstanding amounts by age.
type Buckets struct {
	D0to30  int64  `json:"d0_30_paise"`
	D31to60 int64  `json:"d31_60_paise"`
	D61to90 int64  `json:"d61_90_paise"`
	Over90  int64  `json:"d90_plus_paise"`
	Total   int64  `json:"total_paise"`
	Advance int64  `json:"advance_paise"` // settled more than billed
	Oldest  string `json:"oldest_open_date,omitempty"`
}

// Age settles negative movements FIFO against the oldest positive ones and
// buckets what remains open by its age at asOf.
func Age(ms []Movement, asOf time.Time) Buckets {
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].Date.Before(ms[j].Date) })
	type open struct {
		date time.Time
		amt  int64
	}
	var opens []open
	var credit int64
	for _, m := range ms {
		if m.Amount > 0 {
			opens = append(opens, open{m.Date, m.Amount})
		} else {
			credit += -m.Amount
		}
	}
	for i := range opens {
		if credit == 0 {
			break
		}
		use := min(credit, opens[i].amt)
		opens[i].amt -= use
		credit -= use
	}
	var bk Buckets
	bk.Advance = credit
	for _, o := range opens {
		if o.amt == 0 {
			continue
		}
		if bk.Oldest == "" {
			bk.Oldest = o.date.Format(time.DateOnly)
		}
		days := int(asOf.Sub(o.date).Hours() / 24)
		switch {
		case days <= 30:
			bk.D0to30 += o.amt
		case days <= 60:
			bk.D31to60 += o.amt
		case days <= 90:
			bk.D61to90 += o.amt
		default:
			bk.Over90 += o.amt
		}
		bk.Total += o.amt
	}
	return bk
}
