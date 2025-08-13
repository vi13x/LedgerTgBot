
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/vi13x/bank-lite-cli/internal/domain"
	"github.com/vi13x/bank-lite-cli/internal/storage"
)

type Bank struct {
	store *storage.FileDB
}

func NewBank(store *storage.FileDB) *Bank { return &Bank{store: store} }

func (b *Bank) Register(ctx context.Context, username, password string) (*domain.User, error) {
	username = strings.TrimSpace(username)
	if len(username) < 3 {
		return nil, fmt.Errorf("слишком короткое имя пользователя")
	}
	if len(password) < 4 {
		return nil, fmt.Errorf("пароль слишком короткий")
	}
	if _, err := b.store.GetUserByUsername(username); err == nil {
		return nil, fmt.Errorf("пользователь уже существует")
	}
	h, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	uID, _, _ := b.store.NextIDs()
	u := &domain.User{
		ID:        domain.UserID(fmt.Sprintf("u%06d", uID)),
		Username:  username,
		PassHash:  string(h),
		CreatedAt: time.Now(),
		Role:      domain.RoleUser,
	}
	if err := b.store.CreateUser(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}

func (b *Bank) Login(username, password string) (*domain.User, error) {
	u, err := b.store.GetUserByUsername(username)
	if err != nil {
		return nil, fmt.Errorf("неверный логин или пароль")
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PassHash), []byte(password)) != nil {
		return nil, fmt.Errorf("неверный логин или пароль")
	}
	return u, nil
}

func (b *Bank) CreateAccount(ctx context.Context, owner domain.UserID, currency string) (*domain.Account, error) {
	if currency == "" { currency = "RUB" }
	_, accID, _ := b.store.NextIDs()
	a := &domain.Account{
		ID:        domain.AccountID(fmt.Sprintf("a%06d", accID)),
		Owner:     owner,
		Currency:  currency,
		Balance:   0,
		CreatedAt: time.Now(),
	}
	if err := b.store.CreateAccount(ctx, a); err != nil { return nil, err }
	return a, nil
}

func (b *Bank) Deposit(ctx context.Context, aid domain.AccountID, amount int64, note string) (*domain.Transaction, error) {
	if amount <= 0 { return nil, fmt.Errorf("сумма должна быть > 0") }
	a, err := b.store.GetAccount(aid)
	if err != nil { return nil, err }
	a.Balance += amount
	if err := b.store.UpdateAccount(ctx, a); err != nil { return nil, err }
	_, _, txID := b.store.NextIDs()
	id := domain.TxID(fmt.Sprintf("t%06d", txID))
	t := &domain.Transaction{ID: id, Type: domain.TxDeposit, To: &a.ID, Amount: amount, Currency: a.Currency, Note: note, CreatedAt: time.Now()}
	if err := b.store.CreateTx(ctx, t); err != nil { return nil, err }
	return t, nil
}

func (b *Bank) Withdraw(ctx context.Context, aid domain.AccountID, amount int64, note string) (*domain.Transaction, error) {
	if amount <= 0 { return nil, fmt.Errorf("сумма должна быть > 0") }
	a, err := b.store.GetAccount(aid)
	if err != nil { return nil, err }
	if a.Balance < amount { return nil, fmt.Errorf("недостаточно средств") }
	a.Balance -= amount
	if err := b.store.UpdateAccount(ctx, a); err != nil { return nil, err }
	_, _, txID := b.store.NextIDs()
	id := domain.TxID(fmt.Sprintf("t%06d", txID))
	t := &domain.Transaction{ID: id, Type: domain.TxWithdraw, From: &a.ID, Amount: amount, Currency: a.Currency, Note: note, CreatedAt: time.Now()}
	if err := b.store.CreateTx(ctx, t); err != nil { return nil, err }
	return t, nil
}

func (b *Bank) Transfer(ctx context.Context, from, to domain.AccountID, amount int64, note string) (*domain.Transaction, error) {
	if amount <= 0 { return nil, fmt.Errorf("сумма должна быть > 0") }
	fa, err := b.store.GetAccount(from)
	if err != nil { return nil, err }
	ta, err := b.store.GetAccount(to)
	if err != nil { return nil, err }
	if fa.Currency != ta.Currency { return nil, errors.New("валюты счетов не совпадают") }
	if fa.Balance < amount { return nil, fmt.Errorf("недостаточно средств") }
	fa.Balance -= amount
	ta.Balance += amount
	if err := b.store.UpdateAccount(ctx, fa); err != nil { return nil, err }
	if err := b.store.UpdateAccount(ctx, ta); err != nil { return nil, err }
	_, _, txID := b.store.NextIDs()
	id := domain.TxID(fmt.Sprintf("t%06d", txID))
	t := &domain.Transaction{ID: id, Type: domain.TxTransfer, From: &fa.ID, To: &ta.ID, Amount: amount, Currency: fa.Currency, Note: note, CreatedAt: time.Now()}
	if err := b.store.CreateTx(ctx, t); err != nil { return nil, err }
	return t, nil
}

func (b *Bank) Accounts(uid domain.UserID) ([]*domain.Account, error) {
	return b.store.ListAccountsByUser(uid)
}

func (b *Bank) History(aid domain.AccountID, limit int) ([]*domain.Transaction, error) {
	return b.store.ListTxByAccount(aid, limit)
}
