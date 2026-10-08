package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	pb "github.com/tommyxie2026-tech/computecloud/api/agent/v1"
)

const maxRuntimeUsageUnits = int64(2_000_000_000_000_000)
const estimatedUSDMicros = "usd_micros_client_estimate"

var jsonDecimal = regexp.MustCompile(`^(0|[1-9][0-9]*)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?$`)

func formatUSDmicros(units int64) (string, error) {
	if units < 1 || units > maxRuntimeUsageUnits {
		return "", errors.New("runtime cost budget must be 1..2000000000000000 micro-USD")
	}
	return fmt.Sprintf("%d.%06d", units/1_000_000, units%1_000_000), nil
}

func parseUSDmicros(raw json.RawMessage) (int64, error) {
	match := jsonDecimal.FindStringSubmatch(strings.TrimSpace(string(raw)))
	if match == nil {
		return 0, errors.New("runtime cost must be a non-negative JSON number")
	}
	exponent := int64(0)
	if match[3] != "" {
		var err error
		exponent, err = strconv.ParseInt(match[3], 10, 32)
		if err != nil || exponent < -1000 || exponent > 1000 {
			return 0, errors.New("runtime cost exponent out of range")
		}
	}
	digits := new(big.Int)
	if _, ok := digits.SetString(match[1]+match[2], 10); !ok {
		return 0, errors.New("runtime cost digits invalid")
	}
	if digits.Sign() == 0 {
		return 0, nil
	}
	power := int64(6-len(match[2])) + exponent
	value := new(big.Int).Set(digits)
	ten := big.NewInt(10)
	if power >= 0 {
		value.Mul(value, new(big.Int).Exp(ten, big.NewInt(power), nil))
	} else {
		divisor := new(big.Int).Exp(ten, big.NewInt(-power), nil)
		quotient, remainder := new(big.Int), new(big.Int)
		quotient.QuoRem(value, divisor, remainder)
		value = quotient
		if remainder.Sign() > 0 {
			value.Add(value, big.NewInt(1))
		}
	}
	if !value.IsInt64() || value.Int64() > maxRuntimeUsageUnits {
		return 0, errors.New("runtime cost exceeds accounting range")
	}
	return value.Int64(), nil
}

func applyHTTPRuntimeBudget(profile string, args []string, budget *pb.RuntimeBudget) ([]string, error) {
	out := append([]string(nil), args...)
	if budget == nil {
		return out, nil
	}
	if profile != "claude_http" || budget.RemainingTokenUnits != nil || budget.RemainingCostUnits == nil || budget.GetCostSemantics() != estimatedUSDMicros {
		return nil, errors.New("runtime budget unsupported for provider or dimension")
	}
	for _, arg := range out {
		if arg == "--max-budget-usd" {
			return nil, errors.New("runtime budget argument already present")
		}
	}
	amount, err := formatUSDmicros(budget.GetRemainingCostUnits())
	if err != nil {
		return nil, err
	}
	return append(out, "--max-budget-usd", amount), nil
}
