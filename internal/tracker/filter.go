package tracker

import (
	"net/url"
	"strconv"
	"strings"
)

// Search filters ("filter=<uid>:<op>:<value>") on attributes and data values, as
// central answers them. Checked on DHIS2 2.43 with probe records of chosen values
// (internal/live.TestFilterOperatorParity; docs/FINDINGS.md, "Search filters"):
//
//   - Text compares case-insensitively, accents included ("ÅSA" finds "Åsa"); like
//     and ilike mean "contains", with % and _ taken literally.
//   - Numbers (NUMBER, INTEGER..., PERCENTAGE, UNIT_INTERVAL) compare as numbers for
//     eq, ne, in and the ranges ("10.0" and "1e1" find 10); an operand that is not a
//     number is refused (400). like, sw and ew read the stored text. Central keeps
//     only numbers in these, which the SQL relies on (CAST reads a prefix of text).
//     ieq and nieq compare numbers as text, except on 2.43 attributes (filterKind).
//   - Dates compare as their text, which orders ISO dates.
//   - The negations (ne, nlike, ...) leave out records without a value; null finds
//     those, and a bare "filter=<uid>" means !null.
//   - Ranges on other text follow the central database's collation ("Åsa" sorts with
//     the a's, punctuation is ignored at first), which the hub cannot know: offline
//     they need a connection.
//
// Person searches apply filters in SQL (sqlCond), event searches in Go (match); both
// come from this file and TestFilterSQLMatchesGo holds them to the same answers.

type valueFilter struct {
	uid, op, value string // op lower case; value is empty for null and !null
}

var filterOps = map[string]bool{"eq": true, "ieq": true, "ne": true, "neq": true, "nieq": true, "like": true,
	"ilike": true, "nlike": true, "nilike": true, "sw": true, "ew": true, "in": true, "gt": true, "ge": true,
	"lt": true, "le": true, "null": true, "!null": true}

func parseFilters(q url.Values) ([]valueFilter, *APIError) { return parseFilterParam(q, "filter") }

// parseFilterParam reads one filter parameter: "filter", or on events also
// "filterAttributes" (the person's attributes, as Android sends them).
func parseFilterParam(q url.Values, param string) ([]valueFilter, *APIError) {
	var out []valueFilter
	for _, raw := range q[param] {
		for _, f := range splitUnescaped(raw, ',') {
			parts := splitUnescaped(f, ':')
			if !ValidUID(parts[0]) {
				// A comma splits filters: "eq:Red,Blue" leaves "Blue" as a filter of its
				// own, which central refuses.
				return nil, &APIError{Status: 400, Code: "E1003", Message: "filter=" + raw +
					" is invalid. UID must be an alphanumeric string of 11 characters starting with a letter, but was: " + parts[0]}
			}
			if len(parts) == 1 {
				out = append(out, valueFilter{uid: parts[0], op: "!null"})
				continue
			}
			for i := 1; i < len(parts); {
				op := strings.ToLower(parts[i])
				if rest, ok := strings.CutPrefix(op, "!"); ok && op != "!null" {
					// Central reads "!x" as "nx" (QueryOperator.fromString): !eq is neq,
					// !like nlike; Android's SDK sends !eq. "!in" has no "nin".
					op = "n" + rest
					if op == "nnull" || op == "nne" || op == "nneq" || op == "nnieq" || op == "nnlike" || op == "nnilike" {
						op = "invalid"
					}
				}
				if !filterOps[op] {
					return nil, &APIError{Status: 400, Code: "E1003",
						Message: "filter=" + f + " is invalid. '" + parts[i] + "' is not a valid operator."}
				}
				if op == "null" || op == "!null" {
					out = append(out, valueFilter{uid: parts[0], op: op})
					i++
					continue
				}
				if i+1 >= len(parts) {
					return nil, &APIError{Status: 400, Code: "E1003",
						Message: "filter=" + f + " is invalid. Binary operator '" + parts[i] + "' must have a value."}
				}
				out = append(out, valueFilter{uid: parts[0], op: op, value: parts[i+1]})
				i += 2
			}
		}
	}
	return out, nil
}

// valueKind is how a value type compares.
type valueKind int

const (
	kindText    valueKind = iota // case-insensitive; ranges need central's collation
	kindNumber                   // decimals (central casts to numeric)
	kindInteger                  // integers (central casts to integer: 10.0 is refused)
	kindOrdered                  // dates: ranges as text, which orders ISO dates
	kindMulti                    // MULTI_TEXT: a comma-separated list, matched item by item
)

func kindOf(valueType string) valueKind {
	switch valueType {
	case "NUMBER", "PERCENTAGE", "UNIT_INTERVAL":
		return kindNumber
	case "INTEGER", "INTEGER_POSITIVE", "INTEGER_NEGATIVE", "INTEGER_ZERO_OR_POSITIVE":
		return kindInteger
	case "DATE", "DATETIME", "AGE":
		return kindOrdered
	case "MULTI_TEXT":
		return kindMulti
	}
	return kindText
}

// filterKind is how central compares the filter f on a value of type vt, an attribute
// or (dataValue) a data value, by central's version (2.42 and 2.43 measured with
// TestFilterOperatorParity): MULTI_TEXT is a list only from 2.43; ieq and nieq cast
// numbers only on 2.43 attributes.
func (v *Viewer) filterKind(f valueFilter, vt string, dataValue bool) valueKind {
	k := kindOf(vt)
	modern := v == nil || v.Major == 0 || v.Major >= 43
	if k == kindMulti && !modern {
		k = kindText
	}
	if k.numeric() && (f.op == "ieq" || f.op == "nieq") && (dataValue || !modern) {
		k = kindText
	}
	return k
}

func (k valueKind) numeric() bool { return k == kindNumber || k == kindInteger }

// base folds the operator's variants: ieq is eq, neq and nieq are ne, ilike is like,
// nilike is nlike.
func (f valueFilter) base() string {
	switch f.op {
	case "ieq":
		return "eq"
	case "neq", "nieq":
		return "ne"
	case "ilike":
		return "like"
	case "nilike":
		return "nlike"
	}
	return f.op
}

func (f valueFilter) operands() []string {
	if f.base() == "in" {
		return strings.Split(f.value, ";")
	}
	return []string{f.value}
}

// isNumber reports whether central's cast takes x: BigDecimal for decimals,
// Integer.valueOf for integers.
func isNumber(x string, k valueKind) bool {
	if k == kindInteger {
		_, err := strconv.ParseInt(x, 10, 32)
		return err == nil
	}
	_, err := strconv.ParseFloat(x, 64)
	return err == nil && !strings.ContainsAny(x, "xXpP_") && !strings.EqualFold(strings.TrimLeft(x, "+-"), "inf") &&
		!strings.EqualFold(strings.TrimLeft(x, "+-"), "infinity") && !strings.EqualFold(x, "nan")
}

func numeric(op string) bool {
	switch op {
	case "eq", "ne", "in", "gt", "ge", "lt", "le":
		return true
	}
	return false
}

func ranged(op string) bool { return op == "gt" || op == "ge" || op == "lt" || op == "le" }

// check refuses what central refuses (an operand that is not a number for a number),
// and says when the hub cannot answer offline (a range on text). what is "attribute"
// or "data element".
func (f valueFilter) check(k valueKind, valueType, what string) *APIError {
	op := f.base()
	if k == kindMulti {
		switch f.op {
		case "eq", "ieq", "in", "ne", "neq", "like", "ilike", "sw", "ew", "nlike", "nilike", "null", "!null":
		case "nieq":
			if what == "data element" {
				return &APIError{Status: 400, Message: "Invalid filter: Operator 'NIEQ' is not supported for multi-text DataElement : '" + f.uid + "'."}
			}
			// On attributes central takes nieq as ne (2.43, TestFilterOperatorParity).
		default:
			return &APIError{Status: 400, Message: "Invalid filter: Operator '" + strings.ToUpper(strings.TrimPrefix(f.op, "!")) +
				"' is not supported for multi-text " + map[string]string{"attribute": "TrackedEntityAttribute", "data element": "DataElement"}[what] +
				" : '" + f.uid + "'."}
		}
	}
	if k.numeric() && numeric(op) {
		for _, x := range f.operands() {
			if !isNumber(x, k) {
				return &APIError{Status: 400, Message: "Filter for " + what + " " + f.uid + " is invalid. The " + what +
					" value type is " + valueType + " but the value `" + x + "` is not."}
			}
		}
	}
	if k == kindText && ranged(op) {
		return &APIError{Status: 503, Message: "needs connection: filter " + f.op + " on text follows central's collation",
			Param: "filter operator " + f.op}
	}
	return nil
}

// match applies the filter to a record's values (uid -> value), as sqlCond does.
func (f valueFilter) match(values map[string]string, k valueKind) bool {
	v, ok := values[f.uid]
	if !ok || v == "" {
		return f.op == "null"
	}
	op := f.base()
	if k == kindMulti {
		return f.matchMulti(v)
	}
	switch op {
	case "null":
		return false
	case "!null":
		return true
	case "ne":
		return !valueFilter{uid: f.uid, op: "eq", value: f.value}.match(values, k)
	case "nlike":
		return !strings.Contains(strings.ToLower(v), strings.ToLower(f.value))
	case "like":
		return strings.Contains(strings.ToLower(v), strings.ToLower(f.value))
	case "sw":
		return strings.HasPrefix(strings.ToLower(v), strings.ToLower(f.value))
	case "ew":
		return strings.HasSuffix(strings.ToLower(v), strings.ToLower(f.value))
	}
	if k.numeric() {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return false
		}
		for _, x := range f.operands() {
			y, _ := strconv.ParseFloat(x, 64)
			if compare(op, n < y, n == y) {
				return true
			}
		}
		return false
	}
	lv := strings.ToLower(v)
	for _, x := range f.operands() {
		lx := strings.ToLower(x)
		if compare(op, lv < lx, lv == lx) {
			return true
		}
	}
	return false
}

// matchDataValue is match for an event's data values. A MULTI_TEXT data value that is
// missing passes ne and nlike: central tests "no item matches" on the event's list,
// which is empty (FilterJdbcPredicate.getMultiTextClause; attributes need a value).
func (f valueFilter) matchDataValue(values map[string]string, k valueKind) bool {
	if k == kindMulti && values[f.uid] == "" && (f.base() == "ne" || f.base() == "nlike") {
		return true
	}
	return f.match(values, k)
}

// matchMulti applies the filter to a MULTI_TEXT value v (not empty), as central's
// getMultiTextClause does: the lower-cased value split on commas, item by item.
func (f valueFilter) matchMulti(v string) bool {
	items := strings.Split(strings.ToLower(v), ",")
	any := func(ok func(string) bool) bool {
		for _, it := range items {
			if ok(it) {
				return true
			}
		}
		return false
	}
	x := strings.ToLower(f.value)
	in := func(it string) bool {
		for _, o := range f.operands() {
			if it == strings.ToLower(o) {
				return true
			}
		}
		return false
	}
	switch f.base() {
	case "eq", "in":
		return any(in)
	case "ne":
		return !any(in)
	case "like":
		return any(func(it string) bool { return strings.Contains(it, x) })
	case "sw":
		return any(func(it string) bool { return strings.HasPrefix(it, x) })
	case "ew":
		return any(func(it string) bool { return strings.HasSuffix(it, x) })
	case "nlike":
		return !any(func(it string) bool { return strings.Contains(it, x) })
	case "!null":
		return any(func(it string) bool { return it != "" })
	}
	return false
}

func compare(op string, less, equal bool) bool {
	switch op {
	case "eq", "in":
		return equal
	case "gt":
		return !less && !equal
	case "ge":
		return !less
	case "lt":
		return less
	case "le":
		return less || equal
	}
	return false
}

// sqlCond is the filter as a condition on the person's attribute rows (te_attrs a,
// value_lc is the value in lower case), for EXISTS, or for NOT EXISTS when negate is
// set (null). It means what match does.
func (f valueFilter) sqlCond(k valueKind) (cond string, args []any, negate bool) {
	op := f.base()
	val := "a.value_lc"
	if k.numeric() && numeric(op) && op != "ne" {
		val = "CAST(a.value_lc AS REAL)"
	}
	arg := func(x string) any {
		if k.numeric() && numeric(op) {
			n, _ := strconv.ParseFloat(x, 64)
			return n
		}
		return strings.ToLower(x)
	}
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	lx := esc.Replace(strings.ToLower(f.value))
	if k == kindMulti && op != "null" {
		return f.sqlMulti(esc), nil, false
	}
	switch op {
	case "null":
		return "1", nil, true
	case "!null":
		return "a.value_lc <> ''", nil, false
	case "like":
		return "a.value_lc LIKE ? ESCAPE '\\'", []any{"%" + lx + "%"}, false
	case "nlike":
		return "a.value_lc <> '' AND a.value_lc NOT LIKE ? ESCAPE '\\'", []any{"%" + lx + "%"}, false
	case "sw":
		return "a.value_lc LIKE ? ESCAPE '\\'", []any{lx + "%"}, false
	case "ew":
		return "a.value_lc LIKE ? ESCAPE '\\'", []any{"%" + lx}, false
	case "ne":
		if k.numeric() {
			return "a.value_lc <> '' AND CAST(a.value_lc AS REAL) <> ?", []any{arg(f.value)}, false
		}
		return "a.value_lc <> '' AND a.value_lc <> ?", []any{arg(f.value)}, false
	case "in":
		xs := f.operands()
		marks := strings.TrimSuffix(strings.Repeat("?,", len(xs)), ",")
		for _, x := range xs {
			args = append(args, arg(x))
		}
		return "a.value_lc <> '' AND " + val + " IN (" + marks + ")", args, false
	}
	sqlOp := map[string]string{"eq": "=", "gt": ">", "ge": ">=", "lt": "<", "le": "<="}[op]
	return "a.value_lc <> '' AND " + val + " " + sqlOp + " ?", []any{arg(f.value)}, false
}

// attrSQL adds the person-attribute filters to a search, for the person in column
// person (t.uid, e.te_uid or ev.te_uid), with the candidate lists of selective ones.
func (ix *Index) attrSQL(v *Viewer, w *where, filters []valueFilter, person string) *APIError {
	for _, f := range filters {
		vt := v.valueType(f.uid)
		k := v.filterKind(f, vt, false)
		if err := f.check(k, vt, "attribute"); err != nil {
			return err
		}
		cond, args, negate := f.sqlCond(k)
		if negate {
			w.add("NOT EXISTS (SELECT 1 FROM te_attrs a WHERE a.te_uid = "+person+" AND a.attr = ? AND a.value_lc <> '')", f.uid)
			continue
		}
		w.add("EXISTS (SELECT 1 FROM te_attrs a WHERE a.te_uid = "+person+" AND a.attr = ? AND "+cond+")", append([]any{f.uid}, args...)...)
		if !f.selective() {
			continue
		}
		if list, few := ix.searchCandidates(f.uid, cond, args); few {
			// The filter's few matches drive the search; the condition above still
			// decides, so the answer is the same either way.
			w.add(person+" IN (SELECT value FROM json_each(?))", list)
		}
	}
	return nil
}

// sqlMulti is matchMulti in SQL: an item is bounded by commas in ','||value||','.
// An operand with a comma matches no item, as central compares item by item. The
// operands are literals here (escaped quotes), as the item patterns need them inline.
func (f valueFilter) sqlMulti(esc *strings.Replacer) string {
	lit := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	items := "(',' || a.value_lc || ',')"
	x := strings.ToLower(f.value)
	like := func(pattern string) string { return items + " LIKE " + lit(pattern) + " ESCAPE '\\'" }
	anyEq := func() string {
		var ors []string
		for _, o := range f.operands() {
			if lo := strings.ToLower(o); !strings.Contains(lo, ",") {
				ors = append(ors, like("%,"+esc.Replace(lo)+",%"))
			}
		}
		if len(ors) == 0 {
			return "0"
		}
		return "(" + strings.Join(ors, " OR ") + ")"
	}
	anyLike := func(pattern string) string {
		if strings.Contains(x, ",") {
			return "0"
		}
		return like(pattern)
	}
	switch f.base() {
	case "eq", "in":
		return anyEq()
	case "ne":
		return "a.value_lc <> '' AND NOT " + anyEq()
	case "like":
		return anyLike("%" + esc.Replace(x) + "%")
	case "sw":
		return anyLike("%," + esc.Replace(x) + "%")
	case "ew":
		return anyLike("%" + esc.Replace(x) + ",%")
	case "nlike":
		return "a.value_lc <> '' AND NOT " + anyLike("%"+esc.Replace(x)+"%")
	case "!null":
		return "replace(a.value_lc, ',', '') <> ''"
	}
	return "0"
}

// selective reports whether the filter's matches can drive a search
// (searchCandidates): it finds records by a value, not by its absence.
func (f valueFilter) selective() bool {
	switch f.base() {
	case "eq", "in", "like", "sw", "ew":
		return true
	}
	return false
}
