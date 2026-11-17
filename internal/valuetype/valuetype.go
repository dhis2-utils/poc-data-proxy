// Package valuetype checks a value against a DHIS2 value type as central does, so a
// value the hub takes offline is one central takes at the push (I8c).
package valuetype

import (
	"regexp"
	"strconv"
)

// Central's patterns (DHIS2 MathUtils, the same at 2.42 and 2.43.2.1): plain decimal
// notation (no exponent, no leading "+" or ".", no trailing ".", no spaces) shorter
// than 250 characters, and integers that fit 32 bits. Checked live on 2.43.2.1,
// 17 November 2026: "+5", "5.", ".5", "1e5", "0x10", " 5" and a number of 400 digits
// are refused for NUMBER; 40 digits are taken; 2147483648 and "05" are refused for
// INTEGER.
var (
	numberRe   = regexp.MustCompile(`^(-?0|-?[1-9]\d*)(\.\d+)?$`)
	intRe      = regexp.MustCompile(`^(0|-?[1-9]\d*)$`)
	positiveRe = regexp.MustCompile(`^[1-9]\d*$`)
	zeroPosRe  = regexp.MustCompile(`^(0|[1-9]\d*)$`)
	negativeRe = regexp.MustCompile(`^-[1-9]\d*$`)
)

const numberMaxLength = 250

// Numeric reports whether t is a number type this package checks.
func Numeric(t string) bool {
	switch t {
	case "NUMBER", "UNIT_INTERVAL", "PERCENTAGE", "INTEGER", "INTEGER_POSITIVE", "INTEGER_NEGATIVE", "INTEGER_ZERO_OR_POSITIVE":
		return true
	}
	return false
}

// Valid reports whether v is a valid value of the number type t. Other types are not
// checked here: true.
func Valid(t, v string) bool {
	number := func() (float64, bool) {
		if !numberRe.MatchString(v) || len(v) >= numberMaxLength {
			return 0, false
		}
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil
	}
	int32ok := func(re *regexp.Regexp) bool {
		if !re.MatchString(v) {
			return false
		}
		_, err := strconv.ParseInt(v, 10, 32)
		return err == nil
	}
	switch t {
	case "NUMBER":
		_, ok := number()
		return ok
	case "UNIT_INTERVAL":
		f, ok := number()
		return ok && f >= 0 && f <= 1
	case "PERCENTAGE":
		f, ok := number()
		return ok && f >= 0 && f <= 100
	case "INTEGER":
		return int32ok(intRe)
	case "INTEGER_POSITIVE":
		return int32ok(positiveRe)
	case "INTEGER_NEGATIVE":
		return int32ok(negativeRe)
	case "INTEGER_ZERO_OR_POSITIVE":
		return int32ok(zeroPosRe)
	}
	return true
}
