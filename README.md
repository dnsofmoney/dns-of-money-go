# dns-of-money-go

The Go SDK for [DNS of Money](https://dnsofmoney.com). Resolve FAS-1 `pay:` aliases and call the paid x402 endpoints of the API.

- Go 1.22 or later. Standard library only.
- The SDK never holds keys. Your wallet signs x402 payments through a `Signer` that you implement.
- `ComputeCanonicalHash` and `ComputeSemanticHash` give byte-identical results to the Python package [`a2a-protocol-core`](https://github.com/dnsofmoney/a2a-protocol-core) v0.3.0. Shared test vectors prove it.

```bash
go get github.com/dnsofmoney/dns-of-money-go
```

## Resolve an alias

```go
client := dnsofmoney.NewClient(dnsofmoney.WithAPIKey(os.Getenv("DNSOFMONEY_API_KEY")))

res, err := client.Resolve(ctx, "pay:vendor.alpha")
if err != nil {
	log.Fatal(err)
}
fmt.Println(res.Status, *res.CallerTier)
for _, ep := range res.Endpoints() { // preferred endpoint first, then fallbacks
	fmt.Println(ep.RailName(), ep.Currency)
}
```

An API key is optional. The server redacts the response by the caller's tier:

| Tier | Caller | You get |
|---|---|---|
| 0 | no key | alias, status, IDs, `resolved_at` |
| 1 | starter key | plus display name, entity type, preferred rail |
| 2 | developer, business, enterprise key, or a paid resolve | plus endpoints and compliance |
| 3 | system or agent key | plus routing metadata and `agent_commerce` |

`ResolveWithOptions` sends `POST /resolve` with an amount, a currency and a preferred rail.

### Rate limit and quota headers

Every result carries `Meta`:

```go
if rl := res.Meta.RateLimit; rl != nil { // X-RateLimit-Limit / -Remaining / -Reset
	fmt.Println(rl.Remaining, "calls left this minute")
}
if q := res.Meta.Quota; q != nil { // X-Quota-Limit / -Remaining / -Reset
	fmt.Println(q.Remaining, "calls left this month, resets", q.Reset)
}
```

The server sends the `X-Quota-*` headers only when the monthly quota is on and the caller has a key on a limited plan.

## Pay with x402

A paid route answers the first GET with `402 Payment Required` and a challenge. The challenge lists one `accepts[]` entry for each rail. The SDK picks an entry, asks your `Signer` for the payment header, and sends the GET again with `X-PAYMENT`.

```go
signer := dnsofmoney.SignerFunc(func(ctx context.Context, req dnsofmoney.PaymentRequirements, ch *dnsofmoney.Challenge) (string, error) {
	// Build the x402 PaymentPayload with your own wallet and return it as base64.
	return wallet.SignX402(ctx, req)
})

res, err := client.PaidResolve(ctx, "pay:vendor.alpha", dnsofmoney.PayOptions{
	Signer:         signer,
	Select:         dnsofmoney.SelectNetwork("eip155:8453", ""), // USDC on Base
	IdempotencyKey: newKey(), // optional; 128 bits of entropy or more
})
if err != nil {
	log.Fatal(err)
}
fmt.Println(res.Resolution.Endpoints(), res.PaymentResponse.Transaction, res.PaymentResponse.State)
```

`PayOptions`:

| Field | Use |
|---|---|
| `Signer` | Builds the payment header. Without it, the call returns `*PaymentRequiredError` with the challenge. |
| `Select` | Chooses the accept. The default is `FirstAccept`. `SelectNetwork("xrpl:", "XRP")` matches by prefix. |
| `Currency` | Pins `?currency=` on both legs. Leave it empty to see every rail. |
| `IdempotencyKey` | Sent as `Idempotency-Key`. The server echoes it as `requestKey`. |
| `UsePaymentSignature` | Sends `PAYMENT-SIGNATURE` (x402 v2) instead of `X-PAYMENT`. |

### XRPL: pay first, then prove it

On XRPL the server verifies a payment that is already on the ledger. `XRPLTxHashSigner` wraps a function that submits the payment and returns the hash:

```go
signer := dnsofmoney.XRPLTxHashSigner(func(ctx context.Context, req dnsofmoney.PaymentRequirements) (txHash, payer string, err error) {
	// Pay req.AtomicAmount() drops to req.PayTo.
	// Set InvoiceID = dnsofmoney.InvoiceIDHash(req.InvoiceIDValue()).
	return myXRPL.PayAndWait(ctx, req)
})
```

### Paid routes

| Method | Route | Needs a key |
|---|---|---|
| `PaidResolve(ctx, alias, opts)` | `GET /api/v1/x402/resolve/{alias}` | no |
| `Screen(ctx, target, opts)` | `GET /api/v1/x402/screen/{target}` | no |
| `BuyPlan(ctx, plan, opts)` | `GET /api/v1/x402/plan/{plan}` | yes |
| `PayAlias(ctx, alias, amount, opts)` | `GET /api/v1/x402/pay/{alias}` | yes, to settle |
| `AttestSettledPayment(...)` | `GET /api/v1/x402/pay/{alias}` with a tx hash | yes |
| `ScreenWithPaymentHeader(...)` | screen with a header you built | no |

`FetchChallenge` and `SettleWithHeader` expose the two legs for any other paid path.

`BuyPlan` sells the plan to the tenant of your API key. The server sets the price, and takes USD stablecoins only (`USDC-BASE` by default, or `USDC`, `USDC-SOL`, `USDT0-XLAYER`, `RLUSD`).

```go
client := dnsofmoney.NewClient(dnsofmoney.WithAPIKey(key))
plan, err := client.BuyPlan(ctx, "business", dnsofmoney.PayOptions{Signer: signer, Currency: "USDC-BASE"})
```

## Errors

Every HTTP failure is an `*APIError` with `StatusCode`, `Code`, `Message` and `Meta`. Typed errors wrap it:

| Error | When |
|---|---|
| `*QuotaExceededError` | 402 `quota_exceeded`. Carries `Plan`, `Limit`, `Used`, `ResetAt`, `PayPerCall`, `Upgrade`. |
| `*PaymentRequiredError` | 402 x402 challenge. `Challenge.Error` holds the reason when the server rejected a payment. |
| `*RateLimitError` | 429. Carries `RetryAfter` from the `Retry-After` header. |
| `*X402PayError` | Client side: no matching accept, signer failure, bad challenge. |

Sentinels work with `errors.Is`: `ErrAuthentication`, `ErrAliasNotFound`, `ErrAliasTaken`, `ErrCapReached`, `ErrRateLimited`, `ErrQuotaExceeded`, `ErrPaymentRequired`. The x402 codes are constants: `CodeX402NotPayable`, `CodeX402ResolveNotResolved`, `CodeX402PlanAlreadyHigher` and the rest.

```go
res, err := client.Resolve(ctx, alias)
var quota *dnsofmoney.QuotaExceededError
if errors.As(err, &quota) {
	paid, err := client.PaidResolve(ctx, alias, dnsofmoney.PayOptions{Signer: signer})
	// ...
}
```

Retries: a GET is retried twice on a transport error or a 502, 503 or 504, with a backoff of 0.5 s that doubles. A 4xx or a 500 is not retried. A POST is never retried. Change this with `WithRetries`. The x402 settle leg is a GET and is safe to retry: the server checks idempotency before it verifies, so a re-sent proof never settles twice.

## Payment-intent hashing

```go
h, _ := dnsofmoney.ComputeCanonicalHash(map[string]any{
	"action": "send", "amount": "2.50", "currency": "USD", "alias": "pay:agent.compute",
	"session_id": "s1", // not part of the hash
})
// 9f719d1efcf1acbc7d18e5347d6ee29b6967bcea095b0c50dac0ecc962593e20, same as Python
```

- `NormalizeAction` collapses synonyms: send, pay, transfer and payment become `EXECUTE_PAYMENT`.
- `ComputeSemanticHash(NormalizeMessage(msg))` hashes a whole message.
- `IsValidPayURI` checks the `pay:` grammar.

Pass amounts as strings. If you decode JSON before you hash it, use `json.Decoder.UseNumber`. Then `10` and `10.0` stay apart, as they do in Python. A Go `float64` hashes as a Python float, so `10.0` hashes as `"10.0"`.

## Tests

```bash
go test ./...
go vet ./...
```

`testdata/canonical_vectors.json` is byte-identical to the file the Python and TypeScript suites use. `testdata/extended_vectors.json` adds edge cases. `testdata/gen_extended_vectors.py` makes it from the Python package:

```bash
py -I testdata/gen_extended_vectors.py ../a2a-protocol-core/src
```

`testdata/live_resolve_402_*` is a 402 challenge recorded from production on 2026-11-02. It holds public receive addresses only.

## License

Apache-2.0. See [LICENSE](LICENSE).
