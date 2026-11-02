"""
Generate the Go SDK's extended cross-language vectors from the Python
reference implementation (a2a-protocol-core, the source of truth).

    py -I testdata/gen_extended_vectors.py <path-to-a2a-protocol-core/src>

Writes extended_vectors.json next to this file. Each request is stored as JSON
TEXT, so the Go test decodes the same bytes that json.loads reads here, with
the int/float difference intact (10 vs 10.0). Never hand-edit the hashes.

These cases cover what canonical_vectors.json does not: Python float repr,
bool-as-int, ensure_ascii escapes (including DEL), str.strip() separators,
nested values, big ints, the X-PAYMENT header bytes and the InvoiceID hash.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

sys.path.insert(0, sys.argv[1])

from a2a_protocol_core import (  # noqa: E402
    __version__,
    build_x_payment_header,
    compute_canonical_hash,
    compute_semantic_hash,
    normalize_action,
    normalize_message,
)
from a2a_protocol_core.x402_pay import invoice_id_hash  # noqa: E402

CANONICAL = {
    "float_trailing_zero": '{"action": "send", "amount": 10.0, "currency": "USD", "alias": "pay:x"}',
    "int_amount": '{"action": "send", "amount": 10, "currency": "USD", "alias": "pay:x"}',
    "float_exp_large": '{"amount": 1e16, "currency": "USD"}',
    "float_below_exp": '{"amount": 1e15, "currency": "USD"}',
    "float_exp_small": '{"amount": 1e-05, "currency": "USD"}',
    "float_fixed_small": '{"amount": 0.0001, "currency": "USD"}',
    "float_point_one": '{"amount": 0.1, "currency": "USD"}',
    "float_neg_zero": '{"amount": -0.0, "currency": "USD"}',
    "float_long": '{"amount": 123456.789012345, "currency": "USD"}',
    "big_int": '{"amount": 123456789012345678901234567890, "currency": "USD"}',
    "bool_amount": '{"amount": true, "currency": "USD"}',
    "bool_false_rail": '{"amount": "1", "rail": false}',
    "int_action": '{"action": 5, "amount": "1"}',
    "null_skipped": '{"action": "pay", "amount": "1", "rail": null, "alias": null}',
    "unicode_alias": '{"action": "send", "amount": "1", "alias": "pay:caf\\u00e9"}',
    "astral_currency": '{"amount": "1", "currency": "\\ud83d\\udcb5"}',
    "del_and_controls": '{"amount": "1", "currency": "a\\u007fb\\u0001c\\u001f\\n\\t\\"\\\\"}',
    "nested_category": '{"amount": "1", "payment_category": {"b": 1, "a": [1, 2.0, "x", null, true]}}',
    "separator_whitespace_action": '{"action": "\\u001csend\\u001f", "amount": "1"}',
    "unicode_space_action": '{"action": "\\u3000pay\\u00a0", "amount": "1"}',
    "unknown_action_kept": '{"action": "  Frobnicate ", "amount": "1"}',
    "every_key": (
        '{"amount": "1", "currency": "USD", "rail": "xrpl", "preferred_rail": "xrpl", "alias": "pay:a",'
        ' "alias_uri": "pay:a", "alias_name": "pay:a", "payment_category": "c", "payment_type": "t",'
        ' "action": "status", "memo": "dropped", "extra_field": "dropped"}'
    ),
}

SEMANTIC = {
    "nested": '{"action": "send", "z": {"b": 2, "a": "\\u00e9"}, "list": [true, null, 1.5, 10.0, 7]}',
    "no_action": '{"intent": "pay vendor", "amount": 2.50}',
    "astral": '{"action": "lookup", "note": "\\ud83d\\ude80 launch"}',
}

NORMALIZE_OK = ["\x1cSEND\x1f", "　verify ", "Status"]

X_PAYMENT = [
    {"tx_hash": "B92C23BADE5864569F82BB65B60F84D3B6A8C59A75FC1E75B3DF2A5121A4DA77", "payer": ""},
    {"tx_hash": "EA8159536A8D557492AF64421EBD78BC0C185A31DCC7CC4F1CAE553245BADBDA", "payer": "r3VGLFEurBH1qHUJSHtSG9mRDfYf98wfga"},
    {"tx_hash": "tést\"q", "payer": "p\\x"},
]

INVOICE_IDS = ["61c96b31238bb2cc171d51309bdce26c", "", "café"]


def build() -> dict:
    canonical = []
    for name, text in CANONICAL.items():
        canonical.append({"name": name, "request_json": text, "expected_hash": compute_canonical_hash(json.loads(text))})
    semantic = []
    for name, text in SEMANTIC.items():
        normalized = normalize_message(json.loads(text))
        semantic.append(
            {
                "name": name,
                "message_json": text,
                "normalized_action": normalized.get("action"),
                "expected_hash": compute_semantic_hash(normalized),
            }
        )
    return {
        "_comment": (
            "Extended cross-language vectors for the Go SDK, generated from a2a-protocol-core "
            "by gen_extended_vectors.py. Do not hand-edit."
        ),
        "a2a_protocol_core_version": __version__,
        "canonical_hash": canonical,
        "semantic_hash": semantic,
        "normalize_action_ok": [{"input": s, "output": normalize_action(s)} for s in NORMALIZE_OK],
        "x_payment_header": [
            {**v, "expected": build_x_payment_header(v["tx_hash"], v["payer"] or None)} for v in X_PAYMENT
        ],
        "invoice_id_hash": [{"input": s, "expected": invoice_id_hash(s)} for s in INVOICE_IDS],
    }


if __name__ == "__main__":
    out = Path(__file__).parent / "extended_vectors.json"
    out.write_text(json.dumps(build(), indent=2) + "\n", encoding="utf-8")
    print(f"wrote {out}")
