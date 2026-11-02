# Changelog

## 0.1.0 (unreleased)

First release.

- Resolve: `GET /resolve/{alias}` and `POST /resolve`, with the tiered response and the `X-RateLimit-*` and `X-Quota-*` headers.
- x402: parse a 402 challenge (body `accepts[]` and the `PAYMENT-REQUIRED` header), select an accept, sign through a caller `Signer`, retry with `X-PAYMENT` or `PAYMENT-SIGNATURE`, and decode `PAYMENT-RESPONSE`.
- Paid routes: paid resolve, counterparty screen, plan purchase, pay alias, attest a settled XRPL payment.
- Typed errors for 402 `quota_exceeded`, x402 challenges, 429 and the x402 error codes.
- Canonical and semantic intent hashes, byte-identical to `a2a-protocol-core` 0.3.0.
