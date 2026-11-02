package dnsofmoney

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode"
)

// A2A-009 semantic normalizer and A2A-008 canonical hash. A port of
// a2a_protocol_core.semantic_normalizer and a2a_protocol_core.canonical_hash.
// The Python package is the source of truth. Where the TypeScript port
// differs from it, this file follows Python.

// SynonymMap collapses synonym verbs into canonical action codes.
var SynonymMap = map[string]string{
	"transfer": "EXECUTE_PAYMENT",
	"send":     "EXECUTE_PAYMENT",
	"pay":      "EXECUTE_PAYMENT",
	"payment":  "EXECUTE_PAYMENT",
	"resolve":  "QUERY",
	"lookup":   "QUERY",
	"verify":   "VERIFY",
	"check":    "VERIFY",
	"confirm":  "VERIFY",
	"report":   "REPORT",
	"status":   "REPORT",
}

// CanonicalActions is the closed set of canonical action codes.
var CanonicalActions = map[string]bool{
	"EXECUTE_PAYMENT": true,
	"QUERY":           true,
	"VERIFY":          true,
	"REPORT":          true,
}

// ErrUnknownAction is wrapped by NormalizeAction for an action outside CanonicalActions.
var ErrUnknownAction = fmt.Errorf("dnsofmoney: unknown action")

// pyIsSpace matches Python's str.isspace. Python also counts the separators
// U+001C..U+001F as whitespace, and unicode.IsSpace does not.
func pyIsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// NormalizeAction lowercases and trims rawAction, then collapses synonyms.
// It returns an error that wraps ErrUnknownAction when the result is not canonical.
func NormalizeAction(rawAction string) (string, error) {
	cleaned := strings.ToLower(strings.TrimFunc(rawAction, pyIsSpace))
	canonical, ok := SynonymMap[cleaned]
	if !ok {
		canonical = strings.ToUpper(cleaned)
	}
	if !CanonicalActions[canonical] {
		return "", fmt.Errorf("%w '%s': not in [EXECUTE_PAYMENT QUERY REPORT VERIFY]", ErrUnknownAction, rawAction)
	}
	return canonical, nil
}

// NormalizeMessage returns a shallow copy of message with its "action" field
// normalized. The input is not changed. A non-string action is an error, as
// it is in Python.
func NormalizeMessage(message map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(message))
	for k, v := range message {
		out[k] = v
	}
	raw, ok := out["action"]
	if !ok {
		return out, nil
	}
	s, isStr := raw.(string)
	if !isStr {
		return nil, fmt.Errorf("dnsofmoney: action must be a string, got %T", raw)
	}
	action, err := NormalizeAction(s)
	if err != nil {
		return nil, err
	}
	out["action"] = action
	return out, nil
}

// ComputeSemanticHash is the SHA-256 hex digest of the Python json.dumps
// (sort_keys=True) form of a normalized message.
func ComputeSemanticHash(normalizedMessage map[string]any) (string, error) {
	return sha256Hex(normalizedMessage)
}

// canonicalIncludedKeys are the fields that carry payment meaning. Every
// other field (session_id, request_id, trace_id, timestamp, idempotency_key,
// memo, payload_hash, canonical_hash, created_at, updated_at, ...) is dropped.
var canonicalIncludedKeys = []string{
	"amount",
	"currency",
	"rail",
	"preferred_rail",
	"alias",
	"alias_uri",
	"alias_name",
	"payment_category",
	"payment_type",
	"action",
}

// ComputeCanonicalHash is the SHA-256 hex digest of the semantically
// significant fields of a payment request. Two equivalent payments give the
// same hash, whatever their session, trace or idempotency metadata.
//
// Rules, as in Python:
//   - keep only the included keys, and drop nil values;
//   - turn numbers into their Python str() form. A bool counts as a number,
//     because bool is an int in Python: true becomes "True";
//   - normalize a string action, and keep it raw if it is not recognized;
//   - serialize with sorted keys and Python separators, then SHA-256.
//
// Pass amounts as strings. A float64 that holds a whole number (10.0) hashes
// as "10.0", the way Python hashes a JSON float. Decode JSON with
// json.Decoder.UseNumber to keep the int/float difference of the source.
func ComputeCanonicalHash(paymentRequest map[string]any) (string, error) {
	canonical := make(map[string]any)
	for _, key := range canonicalIncludedKeys {
		val, ok := paymentRequest[key]
		if !ok || isNil(val) {
			continue
		}
		if s, isNum, err := pyNumericStr(val); err != nil {
			return "", err
		} else if isNum {
			val = s
		}
		if key == "action" {
			if s, isStr := val.(string); isStr {
				if action, err := NormalizeAction(s); err == nil {
					val = action
				}
			}
		}
		canonical[key] = val
	}
	return sha256Hex(canonical)
}

// pyNumericStr returns Python's str(val) when val is an int, float or bool.
func pyNumericStr(val any) (string, bool, error) {
	if n, ok := val.(json.Number); ok {
		s, err := pyNumberString(n, true)
		return s, true, err
	}
	v := reflect.ValueOf(val)
	switch v.Kind() {
	case reflect.Bool:
		if v.Bool() {
			return "True", true, nil
		}
		return "False", true, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), true, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(v.Uint(), 10), true, nil
	case reflect.Float32, reflect.Float64:
		return pyFloatRepr(v.Float()), true, nil
	}
	return "", false, nil
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
		return rv.IsNil()
	}
	return false
}

func sha256Hex(v any) (string, error) {
	s, err := pyJSONDumps(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:]), nil
}
