
package storage

import (
	"encoding/json"
	"os"
	"time"
)

type Rates struct {
	Base      string             `json:"base"`
	Pairs     map[string]float64 `json:"pairs"` // e.g. "USD" => 0.0108 when Base=RUB
	UpdatedAt time.Time          `json:"updated_at"`
}

func EnsureRatesFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	r := Rates{
		Base:  "RUB",
		Pairs: map[string]float64{
			"RUB": 1.0,
			"USD": 0.0108,
			"EUR": 0.0100,
			"KZT": 6.0,
		},
		UpdatedAt: time.Now(),
	}
	return SaveRates(path, &r)
}

func LoadRates(path string) (*Rates, error) {
	b, err := os.ReadFile(path)
	if err != nil { return nil, err }
	var r Rates
	if err := json.Unmarshal(b, &r); err != nil { return nil, err }
	return &r, nil
}

func SaveRates(path string, r *Rates) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil { return err }
	return os.WriteFile(path, b, 0o644)
}
