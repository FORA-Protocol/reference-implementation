//go:build integration

package transport_test

import (
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"google.golang.org/protobuf/types/known/structpb"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
)

// The messages every test in this package sends, addressed to this harness's
// Exchange. The envelope itself is written once, in exchangetest, which the
// console's suites build the same messages through; these only fix the
// Exchange the package addresses.
//
// The file carries the integration tag because every caller does: the lint pass
// runs without tags, and a helper it can see but no caller can is reported as
// dead. One untagged test drives a handler below the mount and writes its own
// literal for that reason.

// newPushRequest builds a catalog push under tenantID, sent as callerID.
func newPushRequest(
	tenantID, callerID string, entries []*forav1.ResourceEntry,
) *forav1.PushResourcesRequest {
	return exchangetest.PushRequest(harnessExchangeDomain, tenantID, callerID, entries)
}

// newUsageReport builds a report against the obligation transactionID names.
// billingID is empty for an obligation that carries none.
func newUsageReport(
	idempotencyKey, transactionID, billingID string, usage *forav1.Usage,
) *forav1.UsageReport {
	return exchangetest.UsageReport(harnessExchangeDomain, idempotencyKey, transactionID, billingID, usage)
}

// newResourceQuery builds a discovery query for uris on behalf of requester.
func newResourceQuery(requester *forav1.Requester, uris []string) *forav1.ResourceQuery {
	return exchangetest.ResourceQuery(harnessExchangeDomain, requester, uris)
}

// newRegisterRequest builds an agent registration. data is the free-form
// registration payload the agent supplies; nil for a registration that carries
// none, which is the common case in these tests.
func newRegisterRequest(data *structpb.Struct) *forav1.RegisterRequest {
	return exchangetest.RegisterRequest(harnessExchangeDomain, data, "")
}

// newRegisterRequestWithTerms is newRegisterRequest naming the terms revision
// the caller accepts. An empty digest leaves the field unset, which is the
// "accepted nothing" case an Exchange publishing a digest refuses.
func newRegisterRequestWithTerms(data *structpb.Struct, termsDigest string) *forav1.RegisterRequest {
	return exchangetest.RegisterRequest(harnessExchangeDomain, data, termsDigest)
}

// newAccountStatusRequest builds the account-status call.
func newAccountStatusRequest() *forav1.GetAccountStatusRequest {
	return exchangetest.AccountStatusRequest(harnessExchangeDomain)
}

// newRequester builds the agent identity a query or an execute carries.
func newRequester(id, domain string, scopes ...string) *forav1.Requester {
	return exchangetest.Requester(id, domain, scopes...)
}
