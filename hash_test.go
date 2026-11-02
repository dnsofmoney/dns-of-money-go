package dnsofmoney

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// decodeNumbers decodes JSON with UseNumber, so 10 and 10.0 stay apart as
// they do in Python's json.loads.
func decodeNumbers(t *testing.T, b []byte, v any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sharedVectors is testdata/canonical_vectors.json, byte-identical to the
// file the Python and TypeScript suites assert against.
type sharedVectors struct {
	PayURI []struct {
		Input string `json:"input"`
		Valid bool   `json:"valid"`
	} `json:"pay_uri"`
	NormalizeAction struct {
		OK []struct {
			Input  string `json:"input"`
			Output string `json:"output"`
		} `json:"ok"`
		Error []string `json:"error"`
	} `json:"normalize_action"`
	CanonicalHash []struct {
		Name         string         `json:"name"`
		Request      map[string]any `json:"request"`
		ExpectedHash string         `json:"expected_hash"`
	} `json:"canonical_hash"`
	EquivalenceGroups [][]string `json:"canonical_hash_equivalence_groups"`
	SemanticHash      []struct {
		Name         string         `json:"name"`
		Message      map[string]any `json:"message"`
		Normalized   map[string]any `json:"normalized"`
		ExpectedHash string         `json:"expected_hash"`
	} `json:"semantic_hash"`
}

func loadShared(t *testing.T) sharedVectors {
	var v sharedVectors
	decodeNumbers(t, readFile(t, "testdata/canonical_vectors.json"), &v)
	return v
}

func TestSharedVectorsPayURI(t *testing.T) {
	for _, c := range loadShared(t).PayURI {
		t.Run(c.Input, func(t *testing.T) {
			if got := IsValidPayURI(c.Input); got != c.Valid {
				t.Fatalf("IsValidPayURI(%q) = %v, want %v", c.Input, got, c.Valid)
			}
		})
	}
}

func TestSharedVectorsNormalizeAction(t *testing.T) {
	v := loadShared(t)
	for _, c := range v.NormalizeAction.OK {
		got, err := NormalizeAction(c.Input)
		if err != nil || got != c.Output {
			t.Errorf("NormalizeAction(%q) = %q, %v; want %q", c.Input, got, err, c.Output)
		}
	}
	for _, bad := range v.NormalizeAction.Error {
		if _, err := NormalizeAction(bad); !errors.Is(err, ErrUnknownAction) {
			t.Errorf("NormalizeAction(%q) err = %v, want ErrUnknownAction", bad, err)
		}
	}
}

func TestSharedVectorsCanonicalHash(t *testing.T) {
	v := loadShared(t)
	byName := map[string]string{}
	for _, c := range v.CanonicalHash {
		t.Run(c.Name, func(t *testing.T) {
			got, err := ComputeCanonicalHash(c.Request)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.ExpectedHash {
				t.Fatalf("hash = %s, want %s", got, c.ExpectedHash)
			}
			byName[c.Name] = got
		})
	}
	for _, g := range v.EquivalenceGroups {
		for _, name := range g {
			if byName[name] != byName[g[0]] {
				t.Errorf("%s not equivalent to %s", name, g[0])
			}
		}
	}
}

func TestSharedVectorsSemanticHash(t *testing.T) {
	for _, c := range loadShared(t).SemanticHash {
		t.Run(c.Name, func(t *testing.T) {
			norm, err := NormalizeMessage(c.Message)
			if err != nil {
				t.Fatal(err)
			}
			if norm["action"] != c.Normalized["action"] {
				t.Fatalf("normalized action = %v, want %v", norm["action"], c.Normalized["action"])
			}
			got, err := ComputeSemanticHash(norm)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.ExpectedHash {
				t.Fatalf("hash = %s, want %s", got, c.ExpectedHash)
			}
		})
	}
}

type extendedVectors struct {
	CanonicalHash []struct {
		Name         string `json:"name"`
		RequestJSON  string `json:"request_json"`
		ExpectedHash string `json:"expected_hash"`
	} `json:"canonical_hash"`
	SemanticHash []struct {
		Name             string `json:"name"`
		MessageJSON      string `json:"message_json"`
		NormalizedAction any    `json:"normalized_action"`
		ExpectedHash     string `json:"expected_hash"`
	} `json:"semantic_hash"`
	NormalizeActionOK []struct {
		Input  string `json:"input"`
		Output string `json:"output"`
	} `json:"normalize_action_ok"`
	XPaymentHeader []struct {
		TxHash   string `json:"tx_hash"`
		Payer    string `json:"payer"`
		Expected string `json:"expected"`
	} `json:"x_payment_header"`
	InvoiceIDHash []struct {
		Input    string `json:"input"`
		Expected string `json:"expected"`
	} `json:"invoice_id_hash"`
}

func loadExtended(t *testing.T) extendedVectors {
	var v extendedVectors
	if err := json.Unmarshal(readFile(t, "testdata/extended_vectors.json"), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestExtendedVectorsCanonicalHash(t *testing.T) {
	for _, c := range loadExtended(t).CanonicalHash {
		t.Run(c.Name, func(t *testing.T) {
			var req map[string]any
			decodeNumbers(t, []byte(c.RequestJSON), &req)
			got, err := ComputeCanonicalHash(req)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.ExpectedHash {
				t.Fatalf("hash = %s, want %s", got, c.ExpectedHash)
			}
		})
	}
}

func TestExtendedVectorsSemanticHash(t *testing.T) {
	for _, c := range loadExtended(t).SemanticHash {
		t.Run(c.Name, func(t *testing.T) {
			var msg map[string]any
			decodeNumbers(t, []byte(c.MessageJSON), &msg)
			norm, err := NormalizeMessage(msg)
			if err != nil {
				t.Fatal(err)
			}
			if c.NormalizedAction != nil && norm["action"] != c.NormalizedAction {
				t.Fatalf("action = %v, want %v", norm["action"], c.NormalizedAction)
			}
			got, err := ComputeSemanticHash(norm)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.ExpectedHash {
				t.Fatalf("hash = %s, want %s", got, c.ExpectedHash)
			}
		})
	}
}

func TestExtendedVectorsNormalizeAction(t *testing.T) {
	for _, c := range loadExtended(t).NormalizeActionOK {
		if got, err := NormalizeAction(c.Input); err != nil || got != c.Output {
			t.Errorf("NormalizeAction(%q) = %q, %v; want %q", c.Input, got, err, c.Output)
		}
	}
}

func TestExtendedVectorsXPaymentHeader(t *testing.T) {
	for _, c := range loadExtended(t).XPaymentHeader {
		if got := BuildXPaymentHeader(c.TxHash, c.Payer); got != c.Expected {
			t.Errorf("BuildXPaymentHeader(%q, %q) = %s, want %s", c.TxHash, c.Payer, got, c.Expected)
		}
	}
}

func TestExtendedVectorsInvoiceIDHash(t *testing.T) {
	for _, c := range loadExtended(t).InvoiceIDHash {
		if got := InvoiceIDHash(c.Input); got != c.Expected {
			t.Errorf("InvoiceIDHash(%q) = %s, want %s", c.Input, got, c.Expected)
		}
	}
}

func TestPyFloatRepr(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{2.5, "2.5"}, {10, "10.0"}, {0.1, "0.1"}, {1e16, "1e+16"}, {1e15, "1000000000000000.0"},
		{1e-5, "1e-05"}, {1e-4, "0.0001"}, {1.5e-7, "1.5e-07"}, {-3, "-3.0"}, {1e100, "1e+100"},
		{123456789.125, "123456789.125"},
	}
	for _, c := range cases {
		if got := pyFloatRepr(c.in); got != c.want {
			t.Errorf("pyFloatRepr(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPyJSONDumps(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, "null"},
		{"sorted map", map[string]any{"b": 1, "a": "x"}, `{"a": "x", "b": 1}`},
		{"list", []any{true, false, nil, 1.0}, `[true, false, null, 1.0]`},
		// The backslash is a separate literal so no tool turns the escape into a character.
		{"non-ascii", "é", `"` + `\` + `u00e9"`},
		{"astral", "🚀", `"` + `\` + `ud83d` + `\` + `ude80"`},
		{"del", "\x7f", `"\u007f"`},
		{"control", "\x01\n", `"\u0001\n"`},
		{"string map", map[string]string{"k": "v"}, `{"k": "v"}`},
		{"json number int", json.Number("007"), "7"},
		{"json number float", json.Number("2.50"), "2.5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := pyJSONDumps(c.in)
			if err != nil || got != c.want {
				t.Fatalf("pyJSONDumps = %q, %v; want %q", got, err, c.want)
			}
		})
	}
	if _, err := pyJSONDumps(map[int]string{1: "x"}); err == nil {
		t.Error("map with int keys: want error")
	}
	if _, err := pyJSONDumps(struct{}{}); err == nil {
		t.Error("struct: want error")
	}
}

func TestCanonicalHashGoNativeValues(t *testing.T) {
	// A Go int hashes like a JSON int. A Go float64 hashes like a JSON float.
	fromInt, _ := ComputeCanonicalHash(map[string]any{"amount": 10})
	fromNumber, _ := ComputeCanonicalHash(map[string]any{"amount": json.Number("10")})
	fromFloat, _ := ComputeCanonicalHash(map[string]any{"amount": 10.0})
	fromFloatJSON, _ := ComputeCanonicalHash(map[string]any{"amount": json.Number("10.0")})
	if fromInt != fromNumber {
		t.Error("int and json.Number(10) differ")
	}
	if fromFloat != fromFloatJSON {
		t.Error("float64 and json.Number(10.0) differ")
	}
	if fromInt == fromFloat {
		t.Error("10 and 10.0 must hash apart, as in Python")
	}
}

func TestNormalizeMessage(t *testing.T) {
	in := map[string]any{"action": "send", "x": 1}
	out, err := NormalizeMessage(in)
	if err != nil || out["action"] != "EXECUTE_PAYMENT" {
		t.Fatalf("got %v, %v", out, err)
	}
	if in["action"] != "send" {
		t.Error("input was changed")
	}
	if _, err := NormalizeMessage(map[string]any{"action": 3}); err == nil {
		t.Error("non-string action: want error")
	}
	if _, err := NormalizeMessage(map[string]any{"action": "nope"}); !errors.Is(err, ErrUnknownAction) {
		t.Errorf("unknown action err = %v", err)
	}
}

func TestAssertValidPayURI(t *testing.T) {
	long := "pay:" + strings.Repeat("a", 63) + "." + strings.Repeat("b", 63)
	cases := []struct {
		in string
		ok bool
	}{
		{"pay:vendor.alpha", true},
		{"pay:a\n", false}, // Python accepts this; see README
		{long, false},      // 131 characters
		{"pay:" + strings.Repeat("a", 64), false},
	}
	for _, c := range cases {
		if _, err := AssertValidPayURI(c.in); (err == nil) != c.ok {
			t.Errorf("AssertValidPayURI(%q) err = %v, want ok=%v", c.in, err, c.ok)
		}
	}
}
