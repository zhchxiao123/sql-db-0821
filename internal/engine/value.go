package engine

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ValueKind identifies the runtime type of a stored or computed value. The
// engine follows SQLite's storage classes: NULL, INTEGER, REAL, TEXT and
// BLOB.
type ValueKind int

const (
	Null ValueKind = iota
	Int
	Float
	Text
	Blob
)

// Value is a single cell in a table row or query result.
type Value struct {
	kind     ValueKind
	intVal   int64
	floatVal float64
	textVal  string
}

func NullValue() Value           { return Value{kind: Null} }
func IntValue(v int64) Value     { return Value{kind: Int, intVal: v} }
func FloatValue(v float64) Value { return Value{kind: Float, floatVal: v} }
func TextValue(v string) Value   { return Value{kind: Text, textVal: v} }
func BlobValue(v string) Value   { return Value{kind: Blob, textVal: v} }

func (v Value) Kind() ValueKind { return v.kind }
func (v Value) IsNull() bool    { return v.kind == Null }
func (v Value) Int() int64      { return v.intVal }
func (v Value) Float() float64  { return v.floatVal }
func (v Value) Text() string    { return v.textVal }

// RenderCLI renders a value for the engine command-line output. The format
// follows the sqlite3 CLI: rows are joined with "|", NULL is rendered as the
// literal "NULL" (sqllogictest convention, not sqlite3's empty string), and
// blobs are rendered as their raw bytes.
func (v Value) RenderCLI() string {
	switch v.kind {
	case Null:
		return "NULL"
	case Int:
		return strconv.FormatInt(v.intVal, 10)
	case Float:
		return formatFloatSQLite(v.floatVal)
	case Text, Blob:
		return v.textVal
	}
	return ""
}

// RenderSLT renders a value in the sqllogictest result format for the given
// column type character ('I', 'R' or 'T'). This mirrors the reference
// sqllogictest runner (slt_sqlite.c), which converts each value to the
// requested type: 'I' uses sqlite3_column_int64, 'R' uses
// sqlite3_column_double with "%.3f", 'T' uses sqlite3_column_text. NULL is
// "NULL", empty text is "(empty)" and control characters are "@".
func (v Value) RenderSLT(typeChar byte) string {
	if v.kind == Null {
		return "NULL"
	}
	switch typeChar {
	case 'I':
		switch v.kind {
		case Int:
			return strconv.FormatInt(v.intVal, 10)
		case Float:
			return strconv.FormatInt(int64(v.floatVal), 10)
		case Text, Blob:
			iv, _ := parseLeadingInt(v.textVal)
			return strconv.FormatInt(iv, 10)
		}
	case 'R':
		switch v.kind {
		case Float:
			return fmt.Sprintf("%.3f", v.floatVal)
		case Int:
			return fmt.Sprintf("%.3f", float64(v.intVal))
		case Text, Blob:
			_, r := parseLeadingNumber(v.textVal)
			return fmt.Sprintf("%.3f", r)
		}
	case 'T':
		var s string
		switch v.kind {
		case Int:
			s = strconv.FormatInt(v.intVal, 10)
		case Float:
			s = formatFloatSQLite(v.floatVal)
		case Text, Blob:
			s = v.textVal
		}
		if s == "" {
			return "(empty)"
		}
		var b strings.Builder
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c < ' ' || c > '~' {
				b.WriteByte('@')
			} else {
				b.WriteByte(c)
			}
		}
		return b.String()
	}
	return v.textVal
}

// formatFloatSQLite renders a float the way the sqlite3 CLI does: 15
// significant digits, always showing a decimal point, and a two-digit signed
// exponent. Verified against sqlite3 3.51.0 for the common cases. Inf and
// -Inf render as "Inf"/"-Inf"; NaN renders as the empty string (sqlite3
// treats NaN as NULL in most contexts and prints nothing for it).
func formatFloatSQLite(f float64) string {
	if math.IsNaN(f) {
		return ""
	}
	if math.IsInf(f, 1) {
		return "Inf"
	}
	if math.IsInf(f, -1) {
		return "-Inf"
	}
	if f == 0 {
		return "0.0"
	}
	s := strconv.FormatFloat(f, 'g', 15, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	} else if i := strings.IndexAny(s, "eE"); i >= 0 && !strings.Contains(s[:i], ".") {
		s = s[:i] + ".0" + s[i:]
	}
	return s
}

// Affinity is a column's type affinity, which governs how values are
// converted on INSERT and how comparisons behave (SQLite rules).
type Affinity int

const (
	AffNone Affinity = iota
	AffInteger
	AffReal
	AffNumeric
	AffText
	AffBlob
)

// columnAffinity maps a declared column type to its affinity using the same
// rolling-window rules as sqlite3AffinityType: "INT" wins (checked last,
// breaks), then CHAR/CLOB/TEXT, then BLOB, then REAL/FLOA/DOUB, else
// NUMERIC.
func columnAffinity(typeName string) Affinity {
	aff := AffNumeric
	lower := strings.ToLower(typeName)
	for i := 0; i < len(lower); i++ {
		if i >= 3 {
			switch lower[i-3 : i+1] {
			case "char", "clob", "text":
				aff = AffText
			case "blob":
				if aff == AffNumeric || aff == AffReal {
					aff = AffBlob
				}
			case "real", "floa", "doub":
				if aff == AffNumeric {
					aff = AffReal
				}
			}
		}
		if i >= 2 && lower[i-2:i+1] == "int" {
			return AffInteger
		}
	}
	return aff
}

// isNumericAffinity reports whether aff is one of the numeric affinities.
func isNumericAffinity(aff Affinity) bool {
	return aff == AffInteger || aff == AffReal || aff == AffNumeric
}

// applyAffinity converts v according to a column's affinity on INSERT,
// matching sqlite3's applyAffinity plus the OP_Affinity REAL handling.
func applyAffinity(v Value, aff Affinity) Value {
	switch aff {
	case AffInteger, AffNumeric:
		switch v.kind {
		case Int:
			return v
		case Float:
			// Convert to Int only when the round-trip is exact and the
			// value is not the extreme integer (sqlite3VdbeIntegerAffinity).
			iv := int64(v.floatVal)
			if v.floatVal == float64(iv) && iv > math.MinInt64 && iv < math.MaxInt64 {
				return IntValue(iv)
			}
			return v
		case Text:
			return applyNumericAffinity(v, true)
		}
		return v
	case AffReal:
		switch v.kind {
		case Int:
			return FloatValue(float64(v.intVal))
		case Float:
			return v
		case Text:
			rv := applyNumericAffinity(v, true)
			if rv.kind == Int {
				return FloatValue(float64(rv.intVal))
			}
			return rv
		}
		return v
	case AffText:
		switch v.kind {
		case Int:
			return TextValue(strconv.FormatInt(v.intVal, 10))
		case Float:
			return TextValue(formatFloatSQLite(v.floatVal))
		}
		return v
	}
	return v // AffBlob, AffNone: no conversion
}

// applyNumericAffinity converts a text value to a number if possible,
// matching sqlite3's applyNumericAffinity. With bTryForInt, integral reals
// (e.g. '48.00') are additionally converted to integers.
func applyNumericAffinity(v Value, bTryForInt bool) Value {
	rc, r := parseLeadingNumber(v.textVal)
	if rc <= 0 {
		return v // not a number: keep the text
	}
	if rc == 1 {
		// A pure integer that consumed the whole string.
		iv, irc := parseLeadingInt(v.textVal)
		if irc <= 1 {
			return IntValue(iv)
		}
		// Overflow: alsoAnInt's RealSameAsInt fallback.
		iv2 := realToI64(r)
		if realSameAsInt(r, iv2) {
			return IntValue(iv2)
		}
	}
	if bTryForInt {
		iv := realToI64(r)
		if r == float64(iv) && iv > math.MinInt64 && iv < math.MaxInt64 {
			return IntValue(iv)
		}
	}
	return FloatValue(r)
}

// castValue applies a CAST to the given affinity, matching
// sqlite3VdbeMemCast. Casting is forced: the value is converted even if that
// loses information.
func castValue(v Value, aff Affinity) Value {
	if v.kind == Null {
		return v
	}
	switch aff {
	case AffBlob:
		if v.kind == Text {
			return BlobValue(v.textVal)
		}
		return v
	case AffNumeric:
		return numerify(v)
	case AffInteger:
		return IntValue(intValueOf(v))
	case AffReal:
		return FloatValue(realValueOf(v))
	case AffText:
		switch v.kind {
		case Int:
			return TextValue(strconv.FormatInt(v.intVal, 10))
		case Float:
			return TextValue(formatFloatSQLite(v.floatVal))
		case Blob:
			return TextValue(v.textVal)
		}
		return v
	}
	return v
}

// numerify implements CAST to NUMERIC (sqlite3VdbeMemNumerify).
func numerify(v Value) Value {
	switch v.kind {
	case Int, Float, Null:
		return v
	case Text, Blob:
		rc, r := parseLeadingNumber(v.textVal)
		if rc == 0 || rc == 1 {
			iv, irc := parseLeadingInt(v.textVal)
			if irc <= 1 {
				return IntValue(iv)
			}
		}
		iv := realToI64(r)
		if realSameAsInt(r, iv) {
			return IntValue(iv)
		}
		return FloatValue(r)
	}
	return v
}

// realSameAsInt reports whether the float r is exactly representable as the
// integer i (sqlite3RealSameAsInt).
func realSameAsInt(r float64, i int64) bool {
	if r == 0.0 {
		return true
	}
	return math.Float64bits(r) == math.Float64bits(float64(i)) &&
		i >= -2251799813685248 && i < 2251799813685248
}

// realToI64 converts a float to int64 with saturation, matching
// sqlite3RealToI64.
func realToI64(r float64) int64 {
	if r < -9223372036854774784.0 {
		return math.MinInt64
	}
	if r > 9223372036854774784.0 {
		return math.MaxInt64
	}
	return int64(r)
}

// numericTypeOf classifies v as a number the way sqlite3's numericType does:
// it returns whether v is an integer or a real, along with the numeric value.
func numericTypeOf(v Value) (isInt, isReal bool, i int64, r float64) {
	switch v.kind {
	case Int:
		return true, false, v.intVal, 0
	case Float:
		return false, true, 0, v.floatVal
	case Text, Blob:
		rc, rv := parseLeadingNumber(v.textVal)
		if rc == 0 {
			// Not a valid number: sqlite3 treats it as an integer (0 or the
			// leading integer portion).
			iv, _ := parseLeadingInt(v.textVal)
			return true, false, iv, 0
		}
		if rc == 1 {
			// A pure integer that consumed the whole string.
			iv, irc := parseLeadingInt(v.textVal)
			if irc <= 1 {
				return true, false, iv, 0
			}
			return false, true, 0, rv
		}
		// rc == 2 or -1: a float (possibly with trailing garbage).
		return false, true, 0, rv
	}
	return false, false, 0, 0
}

// realValueOf converts v to a float the way sqlite3VdbeRealValue does.
func realValueOf(v Value) float64 {
	switch v.kind {
	case Int:
		return float64(v.intVal)
	case Float:
		return v.floatVal
	case Text, Blob:
		_, r := parseLeadingNumber(v.textVal)
		return r
	}
	return 0
}

// intValueOf converts v to an int64 the way sqlite3VdbeIntValue does: floats
// truncate toward zero (saturating), text parses the leading integer.
func intValueOf(v Value) int64 {
	switch v.kind {
	case Int:
		return v.intVal
	case Float:
		return realToI64(v.floatVal)
	case Text, Blob:
		iv, _ := parseLeadingInt(v.textVal)
		return iv
	}
	return 0
}

// booleanValueOf returns 0 (false), 1 (true) or 2 (unknown/NULL), matching
// sqlite3VdbeBooleanValue with ifNull=2.
func booleanValueOf(v Value) int {
	switch v.kind {
	case Null:
		return 2
	case Int:
		if v.intVal != 0 {
			return 1
		}
		return 0
	case Float:
		if v.floatVal != 0.0 {
			return 1
		}
		return 0
	case Text, Blob:
		if realValueOf(v) != 0.0 {
			return 1
		}
		return 0
	}
	return 0
}

// andValues implements SQLite's AND: 0 wins over NULL.
func andValues(a, b Value) Value {
	v1 := booleanValueOf(a)
	v2 := booleanValueOf(b)
	andLogic := [9]int{0, 0, 0, 0, 1, 2, 0, 2, 2}
	r := andLogic[v1*3+v2]
	if r == 2 {
		return NullValue()
	}
	return IntValue(int64(r))
}

// orValues implements SQLite's OR: 1 wins over NULL.
func orValues(a, b Value) Value {
	v1 := booleanValueOf(a)
	v2 := booleanValueOf(b)
	orLogic := [9]int{0, 1, 2, 1, 1, 1, 2, 1, 2}
	r := orLogic[v1*3+v2]
	if r == 2 {
		return NullValue()
	}
	return IntValue(int64(r))
}

// notValue implements SQLite's NOT: NULL stays NULL, everything else is
// inverted to 0/1.
func notValue(a Value) Value {
	if a.kind == Null {
		return NullValue()
	}
	if booleanValueOf(a) != 0 {
		return IntValue(0)
	}
	return IntValue(1)
}

// arith implements the binary arithmetic operators + - * / % exactly the way
// sqlite3's OP_Add/Subtract/Multiply/Divide/Remainder do: integer math with
// overflow promotion to REAL, NULL propagation, division by zero → NULL, and
// NaN results → NULL.
func arith(op string, a, b Value) Value {
	if a.kind == Int && b.kind == Int {
		return intMath(op, a.intVal, b.intVal)
	}
	if a.kind == Null || b.kind == Null {
		return NullValue()
	}
	ai, _, aiv, _ := numericTypeOf(a)
	bi, _, biv, _ := numericTypeOf(b)
	if ai && bi {
		return intMath(op, aiv, biv)
	}
	// fp_math
	ra := realValueOf(a)
	rb := realValueOf(b)
	var r float64
	switch op {
	case "+":
		r = ra + rb
	case "-":
		r = ra - rb
	case "*":
		r = ra * rb
	case "/":
		if rb == 0 {
			return NullValue()
		}
		r = ra / rb
	case "%":
		ia := intValueOf(a)
		ib := intValueOf(b)
		if ib == 0 {
			return NullValue()
		}
		if ib == -1 {
			ib = 1
		}
		r = float64(ia % ib)
	}
	if math.IsNaN(r) {
		return NullValue()
	}
	return FloatValue(r)
}

// intMath performs integer arithmetic with overflow detection. On overflow
// the result is promoted to REAL (fp_math), matching sqlite3.
func intMath(op string, a, b int64) Value {
	switch op {
	case "+":
		if (a > 0 && b > 0 && a > math.MaxInt64-b) || (a < 0 && b < 0 && a < math.MinInt64-b) {
			return FloatValue(float64(a) + float64(b))
		}
		return IntValue(a + b)
	case "-":
		if (b > 0 && a < math.MinInt64+b) || (b < 0 && a > math.MaxInt64+b) {
			return FloatValue(float64(a) - float64(b))
		}
		return IntValue(a - b)
	case "*":
		if a == 0 || b == 0 {
			return IntValue(0)
		}
		if (a == math.MinInt64 && b == -1) || (b == math.MinInt64 && a == -1) {
			return FloatValue(float64(a) * float64(b))
		}
		prod := a * b
		if prod/b != a {
			return FloatValue(float64(a) * float64(b))
		}
		return IntValue(prod)
	case "/":
		if b == 0 {
			return NullValue()
		}
		if b == -1 && a == math.MinInt64 {
			return FloatValue(float64(a) / float64(b))
		}
		return IntValue(a / b)
	case "%":
		if b == 0 {
			return NullValue()
		}
		if b == -1 {
			b = 1
		}
		return IntValue(a % b)
	}
	return NullValue()
}

// compareValues orders two non-NULL values by SQLite's storage class rules:
// NULL < numbers < TEXT < BLOB; numbers compare numerically (with exact
// int/float comparison), text and blob compare byte-wise. No text→number
// coercion happens here — that is the job of comparison affinity.
func compareValues(a, b Value) int {
	an := a.kind == Int || a.kind == Float
	bn := b.kind == Int || b.kind == Float
	if an && bn {
		if a.kind == Int && b.kind == Int {
			if a.intVal < b.intVal {
				return -1
			}
			if a.intVal > b.intVal {
				return 1
			}
			return 0
		}
		if a.kind == Float && b.kind == Float {
			if a.floatVal < b.floatVal {
				return -1
			}
			if a.floatVal > b.floatVal {
				return 1
			}
			return 0
		}
		if a.kind == Int {
			return intFloatCompare(a.intVal, b.floatVal)
		}
		return -intFloatCompare(b.intVal, a.floatVal)
	}
	if an {
		return -1 // numbers sort before text and blob
	}
	if bn {
		return 1
	}
	if a.kind == Text && b.kind == Blob {
		return -1 // text sorts before blob
	}
	if a.kind == Blob && b.kind == Text {
		return 1
	}
	if a.textVal < b.textVal {
		return -1
	}
	if a.textVal > b.textVal {
		return 1
	}
	return 0
}

// intFloatCompare compares an int64 and a float64 exactly, matching
// sqlite3IntFloatCompare.
func intFloatCompare(i int64, r float64) int {
	if math.IsNaN(r) {
		return 1 // SQLite treats NaN as NULL; all integers are greater
	}
	if r < -9223372036854775808.0 {
		return 1
	}
	if r >= 9223372036854775808.0 {
		return -1
	}
	y := int64(r)
	if i < y {
		return -1
	}
	if i > y {
		return 1
	}
	if float64(i) < r {
		return -1
	}
	if float64(i) > r {
		return 1
	}
	return 0
}

// parseLeadingNumber parses the leading numeric portion of s the way
// sqlite3AtoF does. It returns a status code and the parsed value:
//
//	0  not a number (or empty string)
//	1  a valid integer, all of s consumed
//	2  a valid float (decimal point or exponent), all of s consumed
//	-1 a valid float followed by trailing garbage
func parseLeadingNumber(s string) (int, float64) {
	i, n := 0, len(s)
	for i < n && isSpace(s[i]) {
		i++
	}
	if i >= n {
		return 0, 0
	}
	sign := 1.0
	if s[i] == '-' {
		sign = -1
		i++
	} else if s[i] == '+' {
		i++
	}
	var sig uint64
	nDigit := 0
	d := 0 // decimal shift
	for i < n && s[i] >= '0' && s[i] <= '9' {
		if sig < (math.MaxUint64-9)/10 {
			sig = sig*10 + uint64(s[i]-'0')
			nDigit++
		} else {
			d++
		}
		i++
	}
	eType := 1 // 1: pure integer, 2: fractional, 3: exponent
	if i < n && s[i] == '.' {
		i++
		eType++
		for i < n && s[i] >= '0' && s[i] <= '9' {
			if sig < (math.MaxUint64-9)/10 {
				sig = sig*10 + uint64(s[i]-'0')
				d--
				nDigit++
			}
			i++
		}
	}
	eValid := true
	esign := 1
	e := 0
	if i < n && (s[i] == 'e' || s[i] == 'E') {
		i++
		eValid = false
		eType++
		if i < n {
			if s[i] == '-' {
				esign = -1
				i++
			} else if s[i] == '+' {
				i++
			}
			for i < n && s[i] >= '0' && s[i] <= '9' {
				e = e*10 + int(s[i]-'0')
				if e > 10000 {
					e = 10000
				}
				i++
				eValid = true
			}
		}
	}
	for i < n && isSpace(s[i]) {
		i++
	}
	val := 0.0
	if sig != 0 {
		val = float64(sig) * math.Pow10(e*esign+d)
	}
	val *= sign
	if i == n && nDigit > 0 && eValid && eType > 0 {
		return eType, val
	}
	if eType >= 2 && (eType == 3 || eValid) && nDigit > 0 {
		return -1, val
	}
	return 0, 0
}

// parseLeadingInt parses the leading integer of s like sqlite3Atoi64:
// optional whitespace, sign, leading zeros, then digits. It returns the
// value (saturated on overflow) and a status code: -1 no digits, 0 valid,
// 1 extra non-space text, 2 overflow (3 for exactly 2^63 positive).
func parseLeadingInt(s string) (int64, int) {
	i, n := 0, len(s)
	for i < n && isSpace(s[i]) {
		i++
	}
	neg := false
	if i < n {
		if s[i] == '-' {
			neg = true
			i++
		} else if s[i] == '+' {
			i++
		}
	}
	start := i
	for i < n && s[i] == '0' {
		i++
	}
	var digits strings.Builder
	for i < n && s[i] >= '0' && s[i] <= '9' {
		digits.WriteByte(s[i])
		i++
	}
	rc := 0
	if digits.Len() == 0 && start == i {
		rc = -1
	} else {
		j := i
		for j < n {
			if !isSpace(s[j]) {
				rc = 1
				break
			}
			j++
		}
	}
	ds := digits.String()
	if len(ds) < 19 {
		u, _ := strconv.ParseUint(ds, 10, 64)
		if neg {
			return -int64(u), rc
		}
		return int64(u), rc
	}
	// 19+ digits: compare against 2^63.
	c := compare2pow63(ds)
	if c < 0 {
		u, _ := strconv.ParseUint(ds, 10, 64)
		if neg {
			return -int64(u), rc
		}
		return int64(u), rc
	}
	if c == 0 && neg {
		return math.MinInt64, rc // exactly -2^63 fits
	}
	if neg {
		return math.MinInt64, 2
	}
	if c == 0 {
		return math.MaxInt64, 3
	}
	return math.MaxInt64, 2
}

// compare2pow63 compares the digit string (leading zeros stripped) against
// "9223372036854775808" (2^63).
func compare2pow63(digits string) int {
	const pow63 = "9223372036854775808"
	if len(digits) > len(pow63) {
		return 1
	}
	if len(digits) < len(pow63) {
		return -1
	}
	if digits < pow63 {
		return -1
	}
	if digits > pow63 {
		return 1
	}
	return 0
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// likeOperand converts a value to its text form for LIKE/GLOB, matching
// sqlite3_value_text. The second return is false for NULL.
func likeOperand(v Value) (string, bool) {
	switch v.kind {
	case Null:
		return "", false
	case Int:
		return strconv.FormatInt(v.intVal, 10), true
	case Float:
		return formatFloatSQLite(v.floatVal), true
	case Text, Blob:
		return v.textVal, true
	}
	return "", false
}

// likeMatch reports whether s matches the LIKE pattern. LIKE is
// case-insensitive for ASCII, % matches any sequence, _ matches one char, and
// escape (if non-zero) escapes the next character.
func likeMatch(pattern, s string, escape byte) bool {
	return patternCompare(pattern, s, '%', '_', true, rune(escape))
}

// globMatch reports whether s matches the GLOB pattern. GLOB is
// case-sensitive, * matches any sequence, ? matches one char, and [class]
// matches a character class.
func globMatch(pattern, s string) bool {
	return patternCompare(pattern, s, '*', '?', false, '[')
}

// patternCompare matches str against pattern using the SQLite
// pattern-matching algorithm (patternCompare in sqlite3.c). matchAll and
// matchOne are the wildcard characters, noCase enables ASCII case folding
// (LIKE), and matchOther is the escape character for LIKE or '[' for GLOB.
func patternCompare(pattern, str string, matchAll, matchOne rune, noCase bool, matchOther rune) bool {
	pat := []rune(pattern)
	s := []rune(str)
	pi, si := 0, 0
	zEscaped := -1 // pattern index of the last escaped char
	for pi < len(pat) {
		c := pat[pi]
		if c == matchAll {
			// Skip consecutive matchAll/matchOne chars, consuming one
			// string char per matchOne.
			pi++
			for pi < len(pat) && (pat[pi] == matchAll || (pat[pi] == matchOne && matchOne != 0)) {
				if pat[pi] == matchOne {
					if si >= len(s) {
						return false
					}
					si++
				}
				pi++
			}
			if pi >= len(pat) {
				return true // matchAll at the end matches
			}
			if pat[pi] == matchOther {
				if matchOther == '[' {
					// "[...]" immediately after "*": slow recursive search.
					for si < len(s) {
						if patternCompare(string(pat[pi:]), string(s[si:]), matchAll, matchOne, noCase, matchOther) {
							return true
						}
						si++
					}
					return false
				}
				// Escaped char after matchAll.
				pi++
				if pi >= len(pat) {
					return false
				}
			}
			// Search the string for the first char matching c and recurse.
			c = pat[pi]
			for si < len(s) {
				if runeEqual(s[si], c, noCase) {
					if patternCompare(string(pat[pi:]), string(s[si:]), matchAll, matchOne, noCase, matchOther) {
						return true
					}
				}
				si++
			}
			return false
		}
		if c == matchOther {
			if matchOther == '[' {
				// GLOB character class.
				if si >= len(s) {
					return false
				}
				cc := s[si]
				si++
				pi++
				invert := false
				if pi < len(pat) && pat[pi] == '^' {
					invert = true
					pi++
				}
				seen := false
				var prior rune
				if pi < len(pat) && pat[pi] == ']' {
					if cc == ']' {
						seen = true
					}
					pi++
				}
				for pi < len(pat) && pat[pi] != ']' {
					if pat[pi] == '-' && pi+1 < len(pat) && pat[pi+1] != ']' && prior > 0 {
						pi++
						if cc >= prior && cc <= pat[pi] {
							seen = true
						}
						prior = 0
					} else {
						if cc == pat[pi] {
							seen = true
						}
						prior = pat[pi]
					}
					pi++
				}
				if pi >= len(pat) || (seen == invert) {
					return false
				}
				continue
			}
			// LIKE escape char: the next pattern char matches literally.
			pi++
			if pi >= len(pat) {
				return false
			}
			c = pat[pi]
			zEscaped = pi
		}
		if si >= len(s) {
			return false
		}
		c2 := s[si]
		si++
		if c == c2 {
			pi++
			continue
		}
		if noCase && toLower(c) == toLower(c2) && c < 0x80 && c2 < 0x80 {
			pi++
			continue
		}
		if c == matchOne && zEscaped != pi && c2 != 0 {
			pi++
			continue
		}
		return false
	}
	return si >= len(s)
}

func runeEqual(a, b rune, noCase bool) bool {
	if a == b {
		return true
	}
	if noCase && a < 0x80 && b < 0x80 {
		return toLower(a) == toLower(b)
	}
	return false
}

func toLower(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}
	return r
}
