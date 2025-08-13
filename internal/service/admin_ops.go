package service

import "bank-lite-cli/internal/domain"

func (b *Bank) CloseAccount(id domain.AccountID) error {
	return b.store.CloseAccount(context.Background(), id)
}
