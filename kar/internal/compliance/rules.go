package compliance

import (
	"fmt"
	"time"

	"github.com/taraju/kar/internal/clients"
	"github.com/taraju/kar/internal/gsp"
	"github.com/taraju/mool/gst"
)

// DefaultEWayThresholdPaise is the Rule 138 consignment value above which an
// e-way bill is required: ₹50,000 (inter-state everywhere, and intra-state in
// states that have not notified a different limit).
const DefaultEWayThresholdPaise int64 = 5000000

// IntraStateThresholdPaise lists states that notified a higher intra-state
// limit, by state code. ⚠ THE USER MUST VERIFY THIS TABLE against each state's
// current notification before relying on it: states change these limits, some
// apply them only to specific goods or within a city, and states not listed
// fall back to ₹50,000. Values below are ₹1,00,000 as last widely published:
//
//	07 Delhi, 08 Rajasthan, 10 Bihar, 19 West Bengal, 27 Maharashtra, 33 Tamil Nadu
var IntraStateThresholdPaise = map[string]int64{
	"07": 10000000, // Delhi
	"08": 10000000, // Rajasthan
	"10": 10000000, // Bihar
	"19": 10000000, // West Bengal
	"27": 10000000, // Maharashtra
	"33": 10000000, // Tamil Nadu
}

// EWayThreshold returns the limit for a movement between two states. For an
// inter-state movement it is interThreshold (the tenant setting, ₹50,000 by
// default); intra-state movements use the state table.
func EWayThreshold(fromState, toState string, interThreshold int64) int64 {
	if interThreshold <= 0 {
		interThreshold = DefaultEWayThresholdPaise
	}
	if fromState != toState {
		return interThreshold
	}
	if v, ok := IntraStateThresholdPaise[fromState]; ok {
		return v
	}
	return DefaultEWayThresholdPaise
}

// ConsignmentValuePaise is the Rule 138 value: taxable value plus taxes of the
// goods lines, excluding exempt / nil-rated / non-GST supplies and services.
func ConsignmentValuePaise(inv clients.Invoice) int64 {
	var v int64
	for _, l := range inv.Lines {
		if IsService(l.HSN) {
			continue
		}
		switch l.TaxCategory {
		case gst.CatExempt, gst.CatNilRated, gst.CatNonGST:
			continue
		}
		v += l.TaxablePaise + l.CGSTPaise + l.SGSTPaise + l.IGSTPaise + l.CessPaise
	}
	if v < 0 {
		return -v
	}
	return v
}

// CheckDocAge enforces the 180-day rule for e-way bills.
func CheckDocAge(invoiceDate string, now time.Time) error {
	d, err := time.ParseInLocation(time.DateOnly, invoiceDate, gsp.IST)
	if err != nil {
		return fmt.Errorf("invoice_date: %w", err)
	}
	if gsp.DocTooOld(d, now) {
		return fmt.Errorf("the document is dated %s, more than %d days ago; an e-way bill can no longer be generated for it",
			invoiceDate, gsp.MaxDocAgeDays)
	}
	return nil
}
