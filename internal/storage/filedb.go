
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/vi13x/bank-lite-cli/internal/domain"
)

var ErrNotFound = errors.New("not found")

type FileDB struct {
	mu   sync.RWMutex
	file *os.File
	snap *domain.Snapshot
	path string
}

func OpenFileDB(path string) (*FileDB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	db := &FileDB{file: f, path: path}
	if err := db.load(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return db, nil
}

func (db *FileDB) Close() error { return db.file.Close() }

func (db *FileDB) load() error {
	info, err := db.file.Stat()
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		db.snap = &domain.Snapshot{
			Version:  1,
			Users:    map[domain.UserID]*domain.User{},
			Accounts: map[domain.AccountID]*domain.Account{},
			Txs:      map[domain.TxID]*domain.Transaction{},
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		return db.flushLocked()
	}
	dec := json.NewDecoder(db.file)
	var snap domain.Snapshot
	if err := dec.Decode(&snap); err != nil {
		return err
	}
	db.snap = &snap
	return nil
}

func (db *FileDB) flushLocked() error {
	if _, err := db.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	enc := json.NewEncoder(db.file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(db.snap); err != nil {
		return err
	}
	// truncate in case new content is shorter
	pos, _ := db.file.Seek(0, io.SeekCurrent)
	if err := db.file.Truncate(pos); err != nil {
		return err
	}
	return db.file.Sync()
}

func (db *FileDB) withWrite(ctx context.Context, fn func(*domain.Snapshot) error) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if err := fn(db.snap); err != nil {
		return err
	}
	db.snap.UpdatedAt = time.Now()
	return db.flushLocked()
}

func (db *FileDB) withRead(fn func(*domain.Snapshot) error) error {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return fn(db.snap)
}

// CRUD helpers
func (db *FileDB) CreateUser(ctx context.Context, u *domain.User) error {
	return db.withWrite(ctx, func(s *domain.Snapshot) error {
		if _, exists := s.Users[u.ID]; exists {
			return fmt.Errorf("user exists")
		}
		s.Users[u.ID] = u
		return nil
	})
}

func (db *FileDB) UpdateUser(ctx context.Context, u *domain.User) error {
	return db.withWrite(ctx, func(s *domain.Snapshot) error {
		if _, ok := s.Users[u.ID]; !ok {
			return ErrNotFound
		}
		s.Users[u.ID] = u
		return nil
	})
}

func (db *FileDB) GetUserByUsername(username string) (*domain.User, error) {
	var out *domain.User
	db.withRead(func(s *domain.Snapshot) error {
		for _, u := range s.Users {
			if u.Username == username {
				copy := *u
				out = &copy
				break
			}
		}
		return nil
	})
	if out == nil { return nil, ErrNotFound }
	return out, nil
}

func (db *FileDB) GetUser(id domain.UserID) (*domain.User, error) {
	var out *domain.User
	db.withRead(func(s *domain.Snapshot) error {
		if u, ok := s.Users[id]; ok {
			copy := *u
			out = &copy
		}
		return nil
	})
	if out == nil { return nil, ErrNotFound }
	return out, nil
}

func (db *FileDB) CreateAccount(ctx context.Context, a *domain.Account) error {
	return db.withWrite(ctx, func(s *domain.Snapshot) error {
		if _, exists := s.Accounts[a.ID]; exists {
			return fmt.Errorf("account exists")
		}
		s.Accounts[a.ID] = a
		if u, ok := s.Users[a.Owner]; ok {
			u.Accounts = append(u.Accounts, a.ID)
		}
		return nil
	})
}

func (db *FileDB) UpdateAccount(ctx context.Context, a *domain.Account) error {
	return db.withWrite(ctx, func(s *domain.Snapshot) error {
		if _, ok := s.Accounts[a.ID]; !ok {
			return ErrNotFound
		}
		s.Accounts[a.ID] = a
		return nil
	})
}

func (db *FileDB) GetAccount(id domain.AccountID) (*domain.Account, error) {
	var out *domain.Account
	db.withRead(func(s *domain.Snapshot) error {
		if a, ok := s.Accounts[id]; ok {
			copy := *a
			out = &copy
		}
		return nil
	})
	if out == nil { return nil, ErrNotFound }
	return out, nil
}

func (db *FileDB) ListAccountsByUser(uid domain.UserID) ([]*domain.Account, error) {
	var out []*domain.Account
	db.withRead(func(s *domain.Snapshot) error {
		for _, a := range s.Accounts {
			if a.Owner == uid && !a.Closed {
				copy := *a
				out = append(out, &copy)
			}
		}
		return nil
	})
	return out, nil
}

func (db *FileDB) CreateTx(ctx context.Context, tx *domain.Transaction) error {
	return db.withWrite(ctx, func(s *domain.Snapshot) error {
		if _, exists := s.Txs[tx.ID]; exists {
			return fmt.Errorf("tx exists")
		}
		s.Txs[tx.ID] = tx
		return nil
	})
}

func (db *FileDB) ListTxByAccount(aid domain.AccountID, limit int) ([]*domain.Transaction, error) {
	var out []*domain.Transaction
	db.withRead(func(s *domain.Snapshot) error {
		for _, t := range s.Txs {
			if (t.From != nil && *t.From == aid) || (t.To != nil && *t.To == aid) {
				copy := *t
				out = append(out, &copy)
			}
		}
		return nil
	})
	// naive order by CreatedAt asc, then trim
	for i := 0; i < len(out); i++ {
		for j := i+1; j < len(out); j++ {
			if out[i].CreatedAt.After(out[j].CreatedAt) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// ID generators
func (db *FileDB) NextIDs() (user, acc, tx int64) {
	var u, a, t int64
	db.withRead(func(s *domain.Snapshot) error {
		u, a, t = s.NextUser+1, s.NextAcc+1, s.NextTx+1
		return nil
	})
	// bump and persist
	_ = db.withWrite(context.Background(), func(s *domain.Snapshot) error {
		s.NextUser, s.NextAcc, s.NextTx = u, a, t
		return nil
	})
	return u, a, t
}
