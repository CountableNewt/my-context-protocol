package snapshotimport

import (
	"errors"
	"math"

	"github.com/CountableNewt/my-context-protocol/services/go/turso"
)

// exactFloatSQL avoids floating-point JSON parameters. Some remote JSON parsers
// round a decimal f64 parameter by one ULP. Integer strings preserve the signed
// 53-bit significand exactly; scaling by powers of two performs exact IEEE-754
// operations, including the final transition into the subnormal range.
// SQL is application-owned and the returned arguments contain integers only.
func exactFloatSQL(value float64) (string, []turso.Value, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "", nil, errors.New("non-finite SQLite REAL is unsupported")
	}
	bits := math.Float64bits(value)
	fraction := bits & ((uint64(1) << 52) - 1)
	exponent := int((bits >> 52) & 0x7ff)
	if exponent != 0 {
		fraction |= uint64(1) << 52
		exponent = exponent - 1023 - 52
	} else {
		exponent = 1 - 1023 - 52
	}
	mantissa := int64(fraction)
	if bits>>63 != 0 {
		mantissa = -mantissa
	}
	sql := "CAST(? AS REAL)"
	args := []turso.Value{turso.Integer(mantissa)}
	if fraction == 0 {
		if bits>>63 != 0 {
			sql = "(" + sql + " * ?)"
			args = append(args, turso.Integer(-1))
		}
		return sql, args, nil
	}
	for exponent != 0 {
		chunk := exponent
		op := "*"
		if chunk < 0 {
			chunk = -chunk
			op = "/"
		}
		if chunk > 60 {
			chunk = 60
		}
		sql = "(" + sql + " " + op + " ?)"
		args = append(args, turso.Integer(int64(1)<<chunk))
		if exponent > 0 {
			exponent -= chunk
		} else {
			exponent += chunk
		}
	}
	return sql, args, nil
}
