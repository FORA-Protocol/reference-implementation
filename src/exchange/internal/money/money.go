// Package money is the deployment ledger's single money-conversion source:
// the asset scale and the exact conversion from rational currency amounts to
// integer minor units. It is CGO-free on purpose, so both the TigerBeetle
// billing adapter (which links the native client) and the Postgres repo layer
// share one arithmetic without the repo inheriting a CGO dependency.
package money

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/shopspring/decimal"
)

// AssetScale is the deployment ledger's power-of-ten asset scale, deliberately
// fixed at 8: the Exchange carries prices at 8 decimal places, so scale 8
// represents every amount the system can produce as an exact integer count of
// minor units, and TigerBeetle asset scales are immutable per ledger. No
// deployment will ever run another scale — this constant is the single place
// the value is written.
const AssetScale uint8 = 8

// ErrAmountNotRepresentable is returned by MinorUnits when a value has finer
// precision than the asset scale can represent exactly — converting it would
// require rounding, so the converter refuses it rather than silently truncate
// a money amount.
var ErrAmountNotRepresentable = errors.New("money: amount not representable at asset scale")

// ScaleFactor returns 10^AssetScale as a big.Int: the multiplier that converts
// a currency amount into integer minor units. Derived from the one constant so
// production code and test fixtures can never disagree on the factor.
func ScaleFactor() *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(AssetScale)), nil)
}

// DecimalString renders an amount as a plain decimal at the asset scale
// (e.g. "12.50000000") — the human-readable form audit details and log lines
// record. nil renders as the empty string; validation is the caller's concern,
// this is presentation only.
func DecimalString(v *big.Rat) string {
	if v == nil {
		return ""
	}
	return v.FloatString(int(AssetScale))
}

// WireString renders an amount of minor units at the asset scale as the
// protocol's canonical money string: "0.1", "1", "0.05", never "0.10000000".
// Every amount the Exchange holds in minor units reaches the wire through it:
// the console's revenue figures and an account's available balances. A price
// the Exchange holds as a decimal goes to helpers.FormatMoney directly, which
// this calls, so both leave in the same form. A negative amount is an error,
// because the wire carries none. A nil amount is an error too.
func WireString(minor *big.Int) (string, error) {
	if minor == nil {
		return "", fmt.Errorf("money: nil amount")
	}
	return helpers.FormatMoney(decimal.NewFromBigInt(minor, -int32(AssetScale)))
}

// MinorUnits converts an exact rational amount to an integer number of minor
// units at the asset scale. A value with finer precision than the scale can
// represent is rejected with ErrAmountNotRepresentable rather than silently
// rounded, so every amount the ledger stores is exact. A nil value is an
// error. Sign-agnostic: negative amounts convert to negative minor units.
//
// This is the single money-conversion source of truth: the TigerBeetle
// adapter's toMinor, the integration-test fixtures (tbtest), and the boot-time
// welcome-credit parser all call it, so the exactness rule cannot diverge
// between what production stores and what a test asserts.
func MinorUnits(v *big.Rat) (*big.Int, error) {
	if v == nil {
		return nil, fmt.Errorf("money: nil amount")
	}
	scaled := new(big.Rat).Mul(v, new(big.Rat).SetInt(ScaleFactor()))
	if !scaled.IsInt() {
		return nil, fmt.Errorf("%w: %s (scale %d)",
			ErrAmountNotRepresentable, v.FloatString(int(AssetScale)+2), AssetScale)
	}
	return new(big.Int).Set(scaled.Num()), nil
}
