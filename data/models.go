package data

import "time"

type Account struct {
	ID      int     `json:"id"`
	Name    string  `json:"name"`
	Balance float64 `json:"balance"`
}

type Transaction struct {
	ID        int       `json:"id"`
	From      int       `json:"from"`
	To        int       `json:"to"`
	Amount    float64   `json:"amount"`
	Timestamp time.Time `json:"timestamp"`
}
