package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	_ "os"
	"strconv"
	"strings"
	"time"

	"github.com/vi13x/bank-lite-cli/internal/domain"
	"github.com/vi13x/bank-lite-cli/internal/service"
)

type Mode int

const (
	ModeExit Mode = iota
	ModeRegister
	ModeLogin
)

type UI struct {
	bank *service.Bank
	in   *bufio.Reader
	out  io.Writer
}

func NewUI(bank *service.Bank, in *bufio.Reader, out io.Writer) *UI {
	return &UI{bank: bank, in: in, out: out}
}

func (ui *UI) SelectMode() Mode {
	fmt.Fprintln(ui.out, "\nВыберите режим:")
	fmt.Fprintln(ui.out, "1) Регистрация")
	fmt.Fprintln(ui.out, "2) Вход")
	fmt.Fprintln(ui.out, "0) Выход")
	fmt.Fprint(ui.out, "> ")
	line := ui.readLine()
	switch strings.TrimSpace(line) {
	case "1":
		return ModeRegister
	case "2":
		return ModeLogin
	default:
		return ModeExit
	}
}

func (ui *UI) HandleRegister() {
	fmt.Fprintln(ui.out, "\n=== Регистрация ===")
	fmt.Fprint(ui.out, "Логин: ")
	login := ui.readLine()
	fmt.Fprint(ui.out, "Пароль: ")
	pass := ui.readPassword()

	u, err := ui.bank.Register(context.Background(), login, pass)
	if err != nil {
		fmt.Fprintln(ui.out, "Ошибка:", err)
		return
	}
	fmt.Fprintf(ui.out, "Ок! Создан пользователь %s\n", u.Username)
	fmt.Fprintln(ui.out, "Создадим первый счет (валюта по умолчанию RUB).")
	if acc, err := ui.bank.CreateAccount(context.Background(), u.ID, "RUB"); err == nil {
		fmt.Fprintf(ui.out, "Счет %s создан.\n", acc.ID)
	}
}

func (ui *UI) HandleLogin() *domain.User {
	fmt.Fprintln(ui.out, "\n=== Вход ===")
	fmt.Fprint(ui.out, "Логин: ")
	login := ui.readLine()
	fmt.Fprint(ui.out, "Пароль: ")
	pass := ui.readPassword()

	u, err := ui.bank.Login(login, pass)
	if err != nil {
		fmt.Fprintln(ui.out, "Ошибка:", err)
		return nil
	}
	fmt.Fprintf(ui.out, "Добро пожаловать, %s!\n", u.Username)
	return u
}

func (ui *UI) HandleSession(u *domain.User) {
	for {
		fmt.Fprintln(ui.out, "\n=== Меню пользователя ===")
		fmt.Fprintln(ui.out, "1) Мои счета")
		fmt.Fprintln(ui.out, "2) Создать счет")
		fmt.Fprintln(ui.out, "3) Пополнить")
		fmt.Fprintln(ui.out, "4) Снять")
		fmt.Fprintln(ui.out, "5) Перевод")
		fmt.Fprintln(ui.out, "6) История по счету")
		fmt.Fprintln(ui.out, "0) Выход из аккаунта")
		fmt.Fprint(ui.out, "> ")
		choice := strings.TrimSpace(ui.readLine())
		switch choice {
		case "1":
			ui.listAccounts(u.ID)
		case "2":
			ui.createAccount(u.ID)
		case "3":
			ui.deposit()
		case "4":
			ui.withdraw()
		case "5":
			ui.transfer()
		case "6":
			ui.history()
		default:
			return
		}
	}
}

func (ui *UI) listAccounts(uid domain.UserID) {
	accs, err := ui.bank.Accounts(uid)
	if err != nil {
		fmt.Fprintln(ui.out, "Ошибка:", err)
		return
	}
	if len(accs) == 0 {
		fmt.Fprintln(ui.out, "У вас нет счетов. Создайте новый.")
		return
	}
	fmt.Fprintln(ui.out, "Ваши счета:")
	for _, a := range accs {
		fmt.Fprintf(ui.out, "- %s  %s  баланс: %s\n", a.ID, a.Currency, formatMoney(a.Balance))
	}
}

func (ui *UI) createAccount(uid domain.UserID) {
	fmt.Fprint(ui.out, "Валюта (RUB по умолчанию): ")
	cur := strings.TrimSpace(ui.readLine())
	if cur == "" {
		cur = "RUB"
	}
	acc, err := ui.bank.CreateAccount(context.Background(), uid, cur)
	if err != nil {
		fmt.Fprintln(ui.out, "Ошибка:", err)
		return
	}
	fmt.Fprintf(ui.out, "Счет %s создан.\n", acc.ID)
}

func (ui *UI) deposit() {
	fmt.Fprint(ui.out, "ID счета: ")
	aid := strings.TrimSpace(ui.readLine())
	amt := ui.readAmount("Сумма (в рублях, например 100.50): ")
	fmt.Fprint(ui.out, "Примечание: ")
	note := ui.readLine()
	if _, err := ui.bank.Deposit(context.Background(), domain.AccountID(aid), amt, note); err != nil {
		fmt.Fprintln(ui.out, "Ошибка:", err)
		return
	}
	fmt.Fprintln(ui.out, "Зачислено.")
}

func (ui *UI) withdraw() {
	fmt.Fprint(ui.out, "ID счета: ")
	aid := strings.TrimSpace(ui.readLine())
	amt := ui.readAmount("Сумма (в рублях, например 50): ")
	fmt.Fprint(ui.out, "Примечание: ")
	note := ui.readLine()
	if _, err := ui.bank.Withdraw(context.Background(), domain.AccountID(aid), amt, note); err != nil {
		fmt.Fprintln(ui.out, "Ошибка:", err)
		return
	}
	fmt.Fprintln(ui.out, "Снято.")
}

func (ui *UI) transfer() {
	fmt.Fprint(ui.out, "Со счета (ID): ")
	from := strings.TrimSpace(ui.readLine())
	fmt.Fprint(ui.out, "На счет (ID): ")
	to := strings.TrimSpace(ui.readLine())
	amt := ui.readAmount("Сумма (в рублях): ")
	fmt.Fprint(ui.out, "Примечание: ")
	note := ui.readLine()
	if _, err := ui.bank.Transfer(context.Background(), domain.AccountID(from), domain.AccountID(to), amt, note); err != nil {
		fmt.Fprintln(ui.out, "Ошибка:", err)
		return
	}
	fmt.Fprintln(ui.out, "Перевод выполнен.")
}

func (ui *UI) history() {
	fmt.Fprint(ui.out, "ID счета: ")
	aid := strings.TrimSpace(ui.readLine())
	fmt.Fprint(ui.out, "Лимит (0 — все): ")
	limStr := strings.TrimSpace(ui.readLine())
	limit, _ := strconv.Atoi(limStr)
	txs, err := ui.bank.History(domain.AccountID(aid), limit)
	if err != nil {
		fmt.Fprintln(ui.out, "Ошибка:", err)
		return
	}
	if len(txs) == 0 {
		fmt.Fprintln(ui.out, "Нет транзакций.")
		return
	}
	fmt.Fprintln(ui.out, "Транзакции:")
	for _, t := range txs {
		var from, to string
		if t.From != nil {
			from = string(*t.From)
		}
		if t.To != nil {
			to = string(*t.To)
		}
		fmt.Fprintf(ui.out, "- %s  %s  %s -> %s  %s  %s\n",
			t.ID, t.Type, empty(from), empty(to), formatMoney(t.Amount), t.CreatedAt.Format(time.RFC3339))
		if strings.TrimSpace(t.Note) != "" {
			fmt.Fprintf(ui.out, "    %s\n", t.Note)
		}
	}
}

func (ui *UI) readLine() string {
	s, _ := ui.in.ReadString('\n')
	return strings.TrimRight(s, "\r\n")
}

func (ui *UI) readPassword() string {
	// for simplicity in cross-platform environments, we don't disable echo.
	return ui.readLine()
}

func (ui *UI) readAmount(prompt string) int64 {
	for {
		fmt.Fprint(ui.out, prompt+" ")
		raw := strings.TrimSpace(ui.readLine())
		if raw == "" {
			return 0
		}
		// replace comma with dot
		raw = strings.ReplaceAll(raw, ",", ".")
		parts := strings.SplitN(raw, ".", 3)
		rubles := parts[0]
		kop := "0"
		if len(parts) >= 2 {
			kop = parts[1]
		}
		if len(kop) == 1 {
			kop = kop + "0"
		}
		valStr := rubles + kop[:min(2, len(kop))]
		n, err := strconv.ParseInt(valStr, 10, 64)
		if err != nil {
			fmt.Fprintln(ui.out, "Неверный формат. Пример: 100.50")
			continue
		}
		return n
	}
}

func formatMoney(minor int64) string {
	sign := ""
	if minor < 0 {
		sign = "-"
		minor = -minor
	}
	r := minor / 100
	k := minor % 100
	return fmt.Sprintf("%s%d.%02d", sign, r, k)
}

func empty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
