package service

import (
	"context"
	"encoding/csv"
	_ "errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"bank-lite-cli/internal/domain"
	"bank-lite-cli/internal/storage"
)

type Bank struct {
	store      *storage.FileDB
	ratesPath  string
	backupsDir string
}

func NewBank(store *storage.FileDB, ratesPath, backupsDir string) *Bank {
	return &Bank{store: store, ratesPath: ratesPath, backupsDir: backupsDir}
}

func (b *Bank) EnsureDefaultAdmin() error {
	// create default admin if none exists
	_, err := b.store.GetUserByUsername("admin")
	if err == nil {
		return nil
	}
	h, _ := bcrypt.GenerateFromPassword([]byte("admin"), bcrypt.DefaultCost)
	uID, _, _ := b.store.NextIDs()
	u := &domain.User{
		ID:        domain.UserID(fmt.Sprintf("u%06d", uID)),
		Username:  "admin",
		PassHash:  string(h),
		CreatedAt: time.Now(),
		Role:      domain.RoleAdmin,
	}
	return b.store.CreateUser(context.Background(), u)
}

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
	u := &domain.User{ID: domain.UserID(fmt.Sprintf("u%06d", uID)), Username: username, PassHash: string(h), CreatedAt: time.Now(), Role: domain.RoleUser}
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
	if currency == "" {
		currency = "RUB"
	}
	_, accID, _ := b.store.NextIDs()
	a := &domain.Account{ID: domain.AccountID(fmt.Sprintf("a%06d", accID)), Owner: owner, Currency: currency, Balance: 0, CreatedAt: time.Now()}
	if err := b.store.CreateAccount(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

func (b *Bank) Deposit(ctx context.Context, aid domain.AccountID, amount int64, note string) (*domain.Transaction, error) {
	if amount <= 0 {
		return nil, fmt.Errorf("сумма должна быть > 0")
	}
	a, err := b.store.GetAccount(aid)
	if err != nil {
		return nil, err
	}
	a.Balance += amount
	if err := b.store.UpdateAccount(ctx, a); err != nil {
		return nil, err
	}
	_, _, txID := b.store.NextIDs()
	id := domain.TxID(fmt.Sprintf("t%06d", txID))
	t := &domain.Transaction{ID: id, Type: domain.TxDeposit, To: &a.ID, Amount: amount, Currency: a.Currency, Note: note, CreatedAt: time.Now()}
	if err := b.store.CreateTx(ctx, t); err != nil {
		return nil, err
	}
	_ = b.BackupNow() // auto-backup on write
	return t, nil
}

func (b *Bank) Withdraw(ctx context.Context, aid domain.AccountID, amount int64, note string) (*domain.Transaction, error) {
	if amount <= 0 {
		return nil, fmt.Errorf("сумма должна быть > 0")
	}
	a, err := b.store.GetAccount(aid)
	if err != nil {
		return nil, err
	}
	if a.Balance < amount {
		return nil, fmt.Errorf("недостаточно средств")
	}
	a.Balance -= amount
	if err := b.store.UpdateAccount(ctx, a); err != nil {
		return nil, err
	}
	_, _, txID := b.store.NextIDs()
	id := domain.TxID(fmt.Sprintf("t%06d", txID))
	t := &domain.Transaction{ID: id, Type: domain.TxWithdraw, From: &a.ID, Amount: amount, Currency: a.Currency, Note: note, CreatedAt: time.Now()}
	if err := b.store.CreateTx(ctx, t); err != nil {
		return nil, err
	}
	_ = b.BackupNow()
	return t, nil
}

func (b *Bank) Transfer(ctx context.Context, from, to domain.AccountID, amount int64, note string) (*domain.Transaction, error) {
	if amount <= 0 {
		return nil, fmt.Errorf("сумма должна быть > 0")
	}
	fa, err := b.store.GetAccount(from)
	if err != nil {
		return nil, err
	}
	ta, err := b.store.GetAccount(to)
	if err != nil {
		return nil, err
	}
	var credit int64 = amount
	if fa.Currency != ta.Currency {
		var convErr error
		credit, convErr = b.ConvertAmount(fa.Currency, ta.Currency, amount)
		if convErr != nil {
			return nil, convErr
		}
		note = strings.TrimSpace(note + fmt.Sprintf(" (FX %s→%s)", fa.Currency, ta.Currency))
	}
	if fa.Balance < amount {
		return nil, fmt.Errorf("недостаточно средств")
	}
	fa.Balance -= amount
	ta.Balance += credit
	if err := b.store.UpdateAccount(ctx, fa); err != nil {
		return nil, err
	}
	if err := b.store.UpdateAccount(ctx, ta); err != nil {
		return nil, err
	}
	_, _, txID := b.store.NextIDs()
	id := domain.TxID(fmt.Sprintf("t%06d", txID))
	t := &domain.Transaction{ID: id, Type: domain.TxTransfer, From: &fa.ID, To: &ta.ID, Amount: amount, Currency: fa.Currency, Note: note, CreatedAt: time.Now()}
	if err := b.store.CreateTx(ctx, t); err != nil {
		return nil, err
	}
	_ = b.BackupNow()
	return t, nil
}

func (b *Bank) Accounts(uid domain.UserID) ([]*domain.Account, error) {
	return b.store.ListAccountsByUser(uid)
}
func (b *Bank) History(aid domain.AccountID, limit int) ([]*domain.Transaction, error) {
	return b.store.ListTxByAccount(aid, limit)
}

// FX
func (b *Bank) loadRates() (*storage.Rates, error) { return storage.LoadRates(b.ratesPath) }
func (b *Bank) saveRates(r *storage.Rates) error   { return storage.SaveRates(b.ratesPath, r) }

func (b *Bank) ConvertAmount(fromCur, toCur string, amount int64) (int64, error) {
	r, err := b.loadRates()
	if err != nil {
		return 0, err
	}
	fromCur = strings.ToUpper(fromCur)
	toCur = strings.ToUpper(toCur)
	if fromCur == toCur {
		return amount, nil
	}
	// Convert amount (minor units) to base currency major, then to target minor with rounding
	// amount is in minor units of fromCur; first to major
	vFrom := float64(amount) / 100.0
	// Map any currency to base via Pairs rate (units of cur per base? Here Pairs holds value of 1 Base in target currency).
	// So to convert FROM X TO Base: vBase = vFrom / rateX
	fromRate, ok1 := r.Pairs[fromCur]
	toRate, ok2 := r.Pairs[toCur]
	if !ok1 || !ok2 {
		return 0, fmt.Errorf("неизвестная валюта")
	}
	// value in Base
	vBase := vFrom / fromRate
	// value in target major = vBase * toRate
	vTarget := vBase * toRate
	minor := int64(vTarget*100.0 + 0.5) // round to nearest
	if minor <= 0 {
		minor = 1
	}
	return minor, nil
}

func (b *Bank) GetRates() (*storage.Rates, error) { return b.loadRates() }
func (b *Bank) SetRate(cur string, rate float64) error {
	r, err := b.loadRates()
	if err != nil {
		return err
	}
	r.Pairs[strings.ToUpper(cur)] = rate
	r.UpdatedAt = time.Now()
	return b.saveRates(r)
}

// Reports
func (b *Bank) ExportAccountCSV(aid domain.AccountID, from, to time.Time, outPath string) (string, error) {
	txs, err := b.store.ListTxByAccount(aid, 0)
	if err != nil {
		return "", err
	}
	// filter by date
	var rows [][]string
	rows = append(rows, []string{"tx_id", "type", "from", "to", "amount_minor", "currency", "created_at", "note"})
	for _, t := range txs {
		if t.CreatedAt.Before(from) || t.CreatedAt.After(to) {
			continue
		}
		fromID, toID := "", ""
		if t.From != nil {
			fromID = string(*t.From)
		}
		if t.To != nil {
			toID = string(*t.To)
		}
		rows = append(rows, []string{string(t.ID), string(t.Type), fromID, toID, fmt.Sprintf("%d", t.Amount), t.Currency, t.CreatedAt.Format(time.RFC3339), t.Note})
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return "", err
	}
	f, err := os.Create(outPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	if err := w.WriteAll(rows); err != nil {
		return "", err
	}
	w.Flush()
	return outPath, nil
}

func (b *Bank) ExportUserSummaryCSV(uid domain.UserID, outPath string) (string, error) {
	accs, err := b.store.ListAccountsByUser(uid)
	if err != nil {
		return "", err
	}
	var rows [][]string
	rows = append(rows, []string{"account_id", "currency", "balance_minor"})
	for _, a := range accs {
		rows = append(rows, []string{string(a.ID), a.Currency, fmt.Sprintf("%d", a.Balance)})
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return "", err
	}
	f, err := os.Create(outPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	w.WriteAll(rows)
	w.Flush()
	return outPath, nil
}

// Backups
func (b *Bank) BackupNow() error {
	// copy db file to backups dir with timestamp
	if b.backupsDir == "" {
		return nil
	}
	if err := os.MkdirAll(b.backupsDir, 0o755); err != nil {
		return err
	}
	ts := time.Now().Format("20060102-150405")
	name := fmt.Sprintf("bank-%s.json", ts)
	src := b.store.Path()
	dst := filepath.Join(b.backupsDir, name)
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, in, 0o600)
}

func (b *Bank) ListBackups() ([]string, error) {
	ents, err := os.ReadDir(b.backupsDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func (b *Bank) RestoreBackup(name string) error {
	src := filepath.Join(b.backupsDir, name)
	bts, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	// overwrite db file
	return os.WriteFile(b.store.Path(), bts, 0o600)
}
