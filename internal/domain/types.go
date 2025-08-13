
package domain

import (
	"time"
)

type UserID string
type AccountID string
type TxID string

type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

type User struct {
	ID        UserID   `json:"id"`
	Username  string   `json:"username"`
	PassHash  string   `json:"pass_hash"`
	CreatedAt time.Time `json:"created_at"`
	Role      Role     `json:"role"`
	Accounts  []AccountID `json:"accounts"`
}

type Account struct {
	ID        AccountID `json:"id"`
	Owner     UserID    `json:"owner"`
	Currency  string    `json:"currency"`
	Balance   int64     `json:"balance"` // minor units (cents)
	CreatedAt time.Time `json:"created_at"`
	Closed    bool      `json:"closed"`
}

type TxType string

const (
	TxDeposit  TxType = "deposit"
	TxWithdraw TxType = "withdraw"
	TxTransfer TxType = "transfer"
)

type Transaction struct {
	ID        TxID      `json:"id"`
	Type      TxType    `json:"type"`
	From      *AccountID `json:"from,omitempty"`
	To        *AccountID `json:"to,omitempty"`
	Amount    int64     `json:"amount"`
	Currency  string    `json:"currency"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

type Snapshot struct {
	Version   int                   `json:"version"`
	Users     map[UserID]*User      `json:"users"`
	Accounts  map[AccountID]*Account `json:"accounts"`
	Txs       map[TxID]*Transaction `json:"txs"`
	NextUser  int64                 `json:"next_user"`
	NextAcc   int64                 `json:"next_acc"`
	NextTx    int64                 `json:"next_tx"`
	CreatedAt time.Time             `json:"created_at"`
	UpdatedAt time.Time             `json:"updated_at"`
}
