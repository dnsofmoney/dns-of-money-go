// Package dnsofmoney is the Go SDK for DNS of Money: resolve FAS-1 pay: aliases
// and call the paid x402 endpoints of the API.
//
// The package uses the standard library only. It mirrors two reference SDKs:
//
//   - the dnsofmoney resolve client (Python and TypeScript, in the public
//     dns-of-money repository), for Resolve and the error types;
//   - a2a-protocol-core v0.3.0 (Python, with a TypeScript port), for the x402
//     pay path, the counterparty screen, the pay: grammar and the intent hashes.
//
// The intelligence lives in the calling agent. This package carries requests
// to the deterministic core and parses what comes back. It never selects a
// rail and never holds keys: x402 payments are signed by a Signer that the
// caller implements, in the caller's process.
//
// ComputeCanonicalHash and ComputeSemanticHash give byte-identical results to
// the Python package. The shared vectors in testdata/ prove it.
package dnsofmoney
