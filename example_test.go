package dnsofmoney_test

import (
	"context"
	"errors"
	"fmt"
	"log"

	dnsofmoney "github.com/dnsofmoney/dns-of-money-go"
)

func ExampleClient_Resolve() {
	client := dnsofmoney.NewClient(dnsofmoney.WithAPIKey("fas_live_..."))
	res, err := client.Resolve(context.Background(), "pay:vendor.alpha")
	var quota *dnsofmoney.QuotaExceededError
	switch {
	case errors.As(err, &quota):
		// The monthly allowance is spent. Pay per call, or buy a plan.
		log.Printf("quota resets %s; pay per call at %s or upgrade at %s", quota.ResetAt, quota.PayPerCall, quota.Upgrade)
		return
	case err != nil:
		log.Fatal(err)
	}
	for _, ep := range res.Endpoints() {
		fmt.Println(ep.RailName(), ep.Currency)
	}
	if q := res.Meta.Quota; q != nil {
		fmt.Printf("%d of %d calls left this month\n", q.Remaining, q.Limit)
	}
}

func ExampleClient_PaidResolve() {
	client := dnsofmoney.NewClient()
	// Your wallet signs. The SDK never sees a key.
	signer := dnsofmoney.SignerFunc(func(ctx context.Context, req dnsofmoney.PaymentRequirements, _ *dnsofmoney.Challenge) (string, error) {
		return myWallet.BuildX402Header(ctx, req) // base64 x402 PaymentPayload
	})
	res, err := client.PaidResolve(context.Background(), "pay:vendor.alpha", dnsofmoney.PayOptions{
		Signer: signer,
		Select: dnsofmoney.SelectNetwork("eip155:8453", ""), // USDC on Base
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.Resolution.Status, res.PaymentResponse.Transaction)
}

func ExampleXRPLTxHashSigner() {
	client := dnsofmoney.NewClient()
	signer := dnsofmoney.XRPLTxHashSigner(func(ctx context.Context, req dnsofmoney.PaymentRequirements) (string, string, error) {
		// Submit an XRP Payment to req.PayTo for req.AtomicAmount() drops, with
		// InvoiceID = dnsofmoney.InvoiceIDHash(req.InvoiceIDValue()). Return the
		// validated tx hash and your r-address.
		return myWallet.PayXRP(ctx, req)
	})
	res, err := client.Screen(context.Background(), "pay:vendor.alpha", dnsofmoney.PayOptions{
		Signer:   signer,
		Currency: "XRP",
	})
	if err != nil {
		log.Fatal(err)
	}
	if res.Verdict == "CLEAR" {
		fmt.Println("safe to pay")
	}
}

func ExampleComputeCanonicalHash() {
	h, err := dnsofmoney.ComputeCanonicalHash(map[string]any{
		"action":     "send", // normalized to EXECUTE_PAYMENT
		"amount":     "2.50",
		"currency":   "USD",
		"alias":      "pay:agent.compute",
		"session_id": "dropped from the hash",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(h)
	// Output: 9f719d1efcf1acbc7d18e5347d6ee29b6967bcea095b0c50dac0ecc962593e20
}

// myWallet stands in for the caller's own wallet code.
var myWallet wallet

type wallet struct{}

func (wallet) BuildX402Header(context.Context, dnsofmoney.PaymentRequirements) (string, error) {
	return "", errors.New("not implemented")
}

func (wallet) PayXRP(context.Context, dnsofmoney.PaymentRequirements) (string, string, error) {
	return "", "", errors.New("not implemented")
}
