package dashboard

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
)

// ExchangeTerms is what the Exchange applies to the publisher: the fee it takes
// from each sale and the usage report it holds buyers to. It is read-only;
// both are the operator's to set.
type ExchangeTerms struct {
	Fee string
	// FeeSource says whether the fee is one set for this publisher or the
	// exchange's standard rate.
	FeeSource string
	// ReportWithin is how long a buyer has to file its usage report.
	ReportWithin string
	// Tolerance is how far a reported quantity may stray from the estimate.
	Tolerance string
	// RequiredFields are the fields a usage report must carry; empty for
	// none.
	RequiredFields string
}

// ExchangeTerms reads the fee and the reporting policy the Exchange applies to
// the publisher.
func (s *Service) ExchangeTerms(ctx context.Context, actor exchangeclient.Actor, nav Nav) (ExchangeTerms, error) {
	got, err := s.reader.PublisherTerms(ctx, exchangeclient.PublisherTermsRequest{
		Actor: actor, PublisherID: nav.PublisherID,
	})
	if err != nil {
		return ExchangeTerms{}, failure(err)
	}
	return ExchangeTerms{
		Fee:            bpsText(got.FeeRateBps),
		FeeSource:      feeSourceText(got.FeeRateSource),
		ReportWithin:   durationText(time.Duration(got.ReportingPolicy.WindowSeconds) * time.Second),
		Tolerance:      percentText(got.ReportingPolicy.QuantityTolerance),
		RequiredFields: strings.Join(got.ReportingPolicy.RequiredFields, ", "),
	}, nil
}

// bpsText writes basis points as a percentage: 1000 is "10%", 250 "2.5%".
func bpsText(bps int) string {
	whole, rest := bps/100, bps%100
	if rest == 0 {
		return strconv.Itoa(whole) + "%"
	}
	frac := strings.TrimRight(strconv.Itoa(100 + rest)[1:], "0")
	return strconv.Itoa(whole) + "." + frac + "%"
}

// percentText writes a fraction as a percentage to two decimals at most, so a
// tolerance of 0.07 reads "7%" and not the float's own 7.000000000000001.
func percentText(fraction float64) string {
	return strconv.FormatFloat(math.Round(fraction*10000)/100, 'f', -1, 64) + "%"
}

func feeSourceText(source string) string {
	switch source {
	case consoleapi.FeeRateSourcePublisher:
		return "set for you"
	case consoleapi.FeeRateSourceTenant:
		return "the exchange's standard rate"
	default:
		return source
	}
}
