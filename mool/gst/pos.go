package gst

// SupplyInput describes a supply for place-of-supply resolution.
type SupplyInput struct {
	Goods         bool   // true for goods (Sec 10 IGST Act), false for services (Sec 12)
	SupplierState string // state code of the supplying store
	BillToState   string // state of the party being invoiced ("" for an unknown walk-in)
	ShipToState   string // state where goods are delivered, when different from bill-to
	Registered    bool   // recipient holds a GSTIN
}

// PlaceOfSupply resolves the place-of-supply state code for the common retail
// and wholesale cases:
//
//   - Goods delivered to a third person on the buyer's instruction
//     (bill-to / ship-to): the buyer's state, Sec 10(1)(b).
//   - Goods otherwise: where the movement terminates (ship-to, else bill-to).
//   - Services to a registered recipient: the recipient's state, Sec 12(2)(a).
//   - Services to an unregistered recipient: their address on record if known,
//     otherwise the supplier's state, Sec 12(2)(b).
//   - Nothing known about the buyer: the supplier's state (walk-in B2C).
//
// Special services (immovable property, events, passenger transport, telecom)
// have their own rules in Sec 12(3)–(14) and must be handled by the caller.
func PlaceOfSupply(in SupplyInput) string {
	if in.Goods {
		switch {
		case in.ShipToState != "" && in.BillToState != "" && in.ShipToState != in.BillToState:
			return in.BillToState
		case in.ShipToState != "":
			return in.ShipToState
		case in.BillToState != "":
			return in.BillToState
		}
		return in.SupplierState
	}
	if in.BillToState != "" {
		return in.BillToState
	}
	return in.SupplierState
}
