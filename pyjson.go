package dnsofmoney

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// pyJSONDumps serializes v exactly like Python's json.dumps(v, sort_keys=True)
// with its default arguments, so a hash over the output is byte-identical to
// the a2a-protocol-core reference.
//
// Python's defaults that differ from encoding/json:
//   - separators are ", " and ": " (with spaces);
//   - ensure_ascii=True: every code point outside 0x20..0x7E is escaped as
//     \uXXXX (lowercase hex, surrogate pairs above U+FFFF). DEL (0x7F) is
//     escaped too;
//   - floats use Python repr: 10.0 stays "10.0", 1e16 becomes "1e+16".
//
// Keys sort by code point. Go sorts strings by UTF-8 bytes, which gives the
// same order. Invalid UTF-8 in a Go string encodes as �.
func pyJSONDumps(v any) (string, error) {
	var b strings.Builder
	if err := pyDump(&b, reflect.ValueOf(v)); err != nil {
		return "", err
	}
	return b.String(), nil
}

func pyDump(b *strings.Builder, v reflect.Value) error {
	if !v.IsValid() {
		b.WriteString("null")
		return nil
	}
	if v.Type() == reflect.TypeOf(json.Number("")) {
		s, err := pyNumberString(json.Number(v.String()), false)
		if err != nil {
			return err
		}
		b.WriteString(s)
		return nil
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			b.WriteString("null")
			return nil
		}
		return pyDump(b, v.Elem())
	case reflect.Bool:
		if v.Bool() {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case reflect.String:
		pyEscapeString(b, v.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		b.WriteString(strconv.FormatInt(v.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		b.WriteString(strconv.FormatUint(v.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		b.WriteString(pyFloatJSON(v.Float()))
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			b.WriteString("null")
			return nil
		}
		b.WriteByte('[')
		for i := 0; i < v.Len(); i++ {
			if i > 0 {
				b.WriteString(", ")
			}
			if err := pyDump(b, v.Index(i)); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("dnsofmoney: cannot hash a map with %s keys", v.Type().Key())
		}
		if v.IsNil() {
			b.WriteString("null")
			return nil
		}
		keys := make([]string, 0, v.Len())
		for _, k := range v.MapKeys() {
			keys = append(keys, k.String())
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			pyEscapeString(b, k)
			b.WriteString(": ")
			if err := pyDump(b, v.MapIndex(reflect.ValueOf(k).Convert(v.Type().Key()))); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("dnsofmoney: cannot hash a value of type %s", v.Type())
	}
	return nil
}

const hexDigits = "0123456789abcdef"

func writeU(b *strings.Builder, r rune) {
	b.WriteString(`\u`)
	for shift := 12; shift >= 0; shift -= 4 {
		b.WriteByte(hexDigits[(r>>uint(shift))&0xf])
	}
}

func pyEscapeString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r >= 0x20 && r <= 0x7e:
				b.WriteRune(r)
			case r > 0xffff:
				c := r - 0x10000
				writeU(b, 0xd800+(c>>10))
				writeU(b, 0xdc00+(c&0x3ff))
			default:
				writeU(b, r)
			}
		}
	}
	b.WriteByte('"')
}

// pyFloatRepr formats f like Python's repr(float).
func pyFloatRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	sci := strconv.FormatFloat(f, 'e', -1, 64) // "-d.ddde±XX"
	mant, expStr, _ := strings.Cut(sci, "e")
	exp, _ := strconv.Atoi(expStr)
	// Python repr switches to exponent form when exp < -4 or exp >= 16.
	if exp < -4 || exp >= 16 {
		sign := "+"
		if exp < 0 {
			sign = "-"
			exp = -exp
		}
		return fmt.Sprintf("%se%s%02d", mant, sign, exp)
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// pyFloatJSON formats f like Python's json.dumps(float).
func pyFloatJSON(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	return pyFloatRepr(f)
}

// pyNumberString maps a JSON number literal to the value Python's json.loads
// would build: an int when the literal has no '.', 'e' or 'E', else a float.
// asStr selects str() output instead of json.dumps output.
func pyNumberString(n json.Number, asStr bool) (string, error) {
	s := n.String()
	if !strings.ContainsAny(s, ".eE") {
		// Python ints have no size limit. Strip a leading '+' or zeros the way int() would.
		neg := strings.HasPrefix(s, "-")
		digits := strings.TrimLeft(strings.TrimPrefix(s, "-"), "0")
		if digits == "" {
			return "0", nil
		}
		if neg {
			return "-" + digits, nil
		}
		return digits, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !math.IsInf(f, 0) {
		return "", fmt.Errorf("dnsofmoney: bad JSON number %q: %w", s, err)
	}
	if asStr {
		return pyFloatRepr(f), nil
	}
	return pyFloatJSON(f), nil
}
