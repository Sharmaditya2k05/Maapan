package gsp

import (
	"regexp"
	"strings"
	"time"
)

// Rule 138(10) CGST Rules: an e-way bill is valid for one day per 200 km (or
// part of it) of road movement; over-dimensional cargo gets one day per 20 km.
const (
	KMPerDay    = 200
	ODCKMPerDay = 20
	MaxDistance = 4000
)

// ValidityDays returns how many days an e-way bill is valid for distanceKM.
// A distance of 0 or less still gets one day.
func ValidityDays(distanceKM int, odc bool) int {
	per := KMPerDay
	if odc {
		per = ODCKMPerDay
	}
	if distanceKM <= 0 {
		return 1
	}
	return (distanceKM + per - 1) / per
}

// ValidUpto applies the Rule 138(10) explanation: validity is counted from
// generation and each day expires at midnight of the day following the date
// of generation. So a one-day bill generated at 10:00 on 24 Oct is valid until
// 23:59:59 IST on 25 Oct.
func ValidUpto(generated time.Time, days int) time.Time {
	if days < 1 {
		days = 1
	}
	g := generated.In(IST)
	end := time.Date(g.Year(), g.Month(), g.Day(), 23, 59, 59, 0, IST)
	return end.AddDate(0, 0, days)
}

// CanExtend reports whether now is inside the extension window: from 8 hours
// before to 8 hours after the current validity ends.
func CanExtend(validUpto, now time.Time) bool {
	return !now.Before(validUpto.Add(-8*time.Hour)) && !now.After(validUpto.Add(8*time.Hour))
}

// MaxDocAgeDays is the 180-day rule (GSTN advisory, effective 1 Jan 2025): an
// e-way bill cannot be generated for a document older than 180 days.
const MaxDocAgeDays = 180

// DocTooOld reports whether a document dated docDate is past the 180-day limit on now.
func DocTooOld(docDate, now time.Time) bool {
	n := now.In(IST)
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, IST)
	d := docDate.In(IST)
	day := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, IST)
	return today.Sub(day) > MaxDocAgeDays*24*time.Hour
}

// Vehicle number formats the NIC e-way system accepts.
var vehiclePatterns = []*regexp.Regexp{
	regexp.MustCompile(`^[A-Z]{2}[0-9]{1,2}[A-Z]{0,3}[0-9]{4}$`), // MH12AB1234, DL1CAB1234, KA011234
	regexp.MustCompile(`^[A-Z]{3}[0-9]{3,4}$`),                   // old format ABC1234
	regexp.MustCompile(`^[0-9]{2}BH[0-9]{4}[A-Z]{1,2}$`),         // Bharat series 22BH1234AA
	regexp.MustCompile(`^TR[A-Z0-9]{6,13}$`),                     // temporary registration
	regexp.MustCompile(`^TM[A-Z0-9]{6}$`),                        // temporary (trade certificate)
	regexp.MustCompile(`^DF[A-Z0-9]{6,13}$`),                     // defence vehicles
	regexp.MustCompile(`^(BP|NP)[A-Z0-9]{6,13}$`),                // Bhutan / Nepal vehicles
}

// NormalizeVehicle uppercases and strips spaces, dashes and dots.
func NormalizeVehicle(v string) string {
	return strings.ToUpper(strings.NewReplacer(" ", "", "-", "", ".", "").Replace(strings.TrimSpace(v)))
}

// ValidVehicle reports whether a (normalised) vehicle number is acceptable.
func ValidVehicle(v string) bool {
	v = NormalizeVehicle(v)
	if len(v) < 6 || len(v) > 15 {
		return false
	}
	for _, p := range vehiclePatterns {
		if p.MatchString(v) {
			return true
		}
	}
	return false
}
