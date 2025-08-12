package data

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type Bank struct {
	Accounts     []Account     `json:"accounts"`
	Transactions []Transaction `json:"transactions"`
	filePath     string
}

func InitStorage(filePath string) (*Bank, error) {
	b := &Bank{filePath: filePath}

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		if err := b.save(); err != nil {
			return nil, err
		}
		return b, nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, b); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func (b *Bank) save() error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(b.filePath, data, 0644)
}

func (b *Bank) CreateAccount(name string) (*Account, error) {
	id := len(b.Accounts) + 1
	acc := Account{ID: id, Name: name, Balance: 0}
	b.Accounts = append(b.Accounts, acc)
	return &acc, b.save()
}

func (b *Bank) Deposit(id int, amount float64) error {
	if amount <= 0 {
		return fmt.Errorf("сумма должна быть положительной")
	}
	for i := range b.Accounts {
		if b.Accounts[i].ID == id {
			b.Accounts[i].Balance += amount
			b.Transactions = append(b.Transactions, Transaction{
				ID:        len(b.Transactions) + 1,
				From:      0,
				To:        id,
				Amount:    amount,
				Timestamp: time.Now(),
			})
			return b.save()
		}
	}
	return fmt.Errorf("счёт %d не найден", id)
}

func (b *Bank) Transfer(from, to int, amount float64) error {
	if amount <= 0 {
		return fmt.Errorf("сумма должна быть положительной")
	}
	if from == to {
		return fmt.Errorf("нельзя перевести на тот же счёт")
	}

	var fromAcc, toAcc *Account
	for i := range b.Accounts {
		if b.Accounts[i].ID == from {
			fromAcc = &b.Accounts[i]
		}
		if b.Accounts[i].ID == to {
			toAcc = &b.Accounts[i]
		}
	}
	if fromAcc == nil || toAcc == nil {
		return fmt.Errorf("один из счетов не найден")
	}
	if fromAcc.Balance < amount {
		return fmt.Errorf("недостаточно средств")
	}

	fromAcc.Balance -= amount
	toAcc.Balance += amount
	b.Transactions = append(b.Transactions, Transaction{
		ID:        len(b.Transactions) + 1,
		From:      from,
		To:        to,
		Amount:    amount,
		Timestamp: time.Now(),
	})
	return b.save()
}

func (b *Bank) GetAccount(id int) (*Account, error) {
	for _, acc := range b.Accounts {
		if acc.ID == id {
			return &acc, nil
		}
	}
	return nil, fmt.Errorf("счёт %d не найден", id)
}

func (b *Bank) GetTransactionHistory(id int) ([]*Transaction, error) {
	var history []*Transaction
	for i := len(b.Transactions) - 1; i >= 0 && len(history) < 10; i-- {
		t := b.Transactions[i]
		if t.From == id || t.To == id {
			history = append(history, &t)
		}
	}
	return history, nil
}
