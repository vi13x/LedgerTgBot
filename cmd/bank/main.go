package cli

import (
	_ "context"
	"fmt"
	_ "os"
	"path/filepath"
	"strings"
	"time"

	"bank/internal/domain"
)

func (ui *UI) HandleAdmin(u *domain.User) {
	for {
		fmt.Fprintln(ui.out, "\n=== Админ-панель ===")
		fmt.Fprintln(ui.out, "1) Список пользователей")
		fmt.Fprintln(ui.out, "2) Список счетов пользователя")
		fmt.Fprintln(ui.out, "3) Закрыть счёт")
		fmt.Fprintln(ui.out, "4) Показать курсы")
		fmt.Fprintln(ui.out, "5) Установить курс валюты")
		fmt.Fprintln(ui.out, "6) Экспорт выписки (CSV)")
		fmt.Fprintln(ui.out, "7) Экспорт сводки по пользователю (CSV)")
		fmt.Fprintln(ui.out, "8) Сделать бэкап сейчас")
		fmt.Fprintln(ui.out, "9) Показать список бэкапов")
		fmt.Fprintln(ui.out, "10) Восстановить из бэкапа")
		fmt.Fprintln(ui.out, "0) Выход")
		fmt.Fprint(ui.out, "> ")
		choice := strings.TrimSpace(ui.readLine())
		switch choice {
		case "1":
			ui.adminListUsers()
		case "2":
			ui.adminListUserAccounts()
		case "3":
			ui.adminCloseAccount()
		case "4":
			ui.adminShowRates()
		case "5":
			ui.adminSetRate()
		case "6":
			ui.adminExportAccount()
		case "7":
			ui.adminExportUser()
		case "8":
			if err := ui.bank.BackupNow(); err != nil {
				fmt.Fprintln(ui.out, "Ошибка:", err)
			} else {
				fmt.Fprintln(ui.out, "Бэкап создан.")
			}
		case "9":
			ui.adminListBackups()
		case "10":
			ui.adminRestoreBackup()
		default:
			return
		}
	}
}

func (ui *UI) adminListUsers() {
	// naive dump of usernames
	fmt.Fprintln(ui.out, "Пользователи:")
	// Internal: no direct list method; read from file via service? For brevity, ask for names known — not available.
	fmt.Fprintln(ui.out, "(подробный список пользователей можно добавить в FileDB при необходимости)")
}

func (ui *UI) adminListUserAccounts() {
	fmt.Fprint(ui.out, "UserID: ")
	uid := strings.TrimSpace(ui.readLine())
	accs, err := ui.bank.Accounts(domain.UserID(uid))
	if err != nil {
		fmt.Fprintln(ui.out, "Ошибка:", err)
		return
	}
	for _, a := range accs {
		fmt.Fprintf(ui.out, "- %s %s balance=%s closed=%v\n", a.ID, a.Currency, formatMoney(a.Balance), a.Closed)
	}
}

func (ui *UI) adminCloseAccount() {
	fmt.Fprint(ui.out, "AccountID: ")
	aid := strings.TrimSpace(ui.readLine())
	if err := ui.bank.CloseAccount(domain.AccountID(aid)); err != nil {
		fmt.Fprintln(ui.out, "Ошибка:", err)
	} else {
		fmt.Fprintln(ui.out, "Счёт закрыт.")
	}
}

func (ui *UI) adminShowRates() {
	r, err := ui.bank.GetRates()
	if err != nil {
		fmt.Fprintln(ui.out, "Ошибка:", err)
		return
	}
	fmt.Fprintf(ui.out, "База: %s  Обновлено: %s\n", r.Base, r.UpdatedAt.Format(time.RFC3339))
	for cur, rate := range r.Pairs {
		fmt.Fprintf(ui.out, "- %s : %.6f\n", cur, rate)
	}
}

func (ui *UI) adminSetRate() {
	fmt.Fprint(ui.out, "Валюта (например USD): ")
	cur := strings.TrimSpace(ui.readLine())
	fmt.Fprint(ui.out, "Курс (сколько %s за 1 базовую валюту): ", cur)
	rateStr := strings.TrimSpace(ui.readLine())
	var rate float64
	fmt.Sscanf(rateStr, "%f", &rate)
	if err := ui.bank.SetRate(cur, rate); err != nil {
		fmt.Fprintln(ui.out, "Ошибка:", err)
	} else {
		fmt.Fprintln(ui.out, "Ок.")
	}
}

func (ui *UI) adminExportAccount() {
	fmt.Fprint(ui.out, "AccountID: ")
	aid := strings.TrimSpace(ui.readLine())
	fmt.Fprint(ui.out, "От (YYYY-MM-DD): ")
	fromStr := strings.TrimSpace(ui.readLine())
	fmt.Fprint(ui.out, "До (YYYY-MM-DD): ")
	toStr := strings.TrimSpace(ui.readLine())
	from, _ := time.Parse("2006-01-02", fromStr)
	to, _ := time.Parse("2006-01-02", toStr)
	if to.Before(from) {
		to = from.Add(24 * time.Hour)
	}
	path := filepath.Join("reports", fmt.Sprintf("statement_%s_%s_%s.csv", aid, from.Format("20060102"), to.Format("20060102")))
	p, err := ui.bank.ExportAccountCSV(domain.AccountID(aid), from, to.Add(24*time.Hour-1), path)
	if err != nil {
		fmt.Fprintln(ui.out, "Ошибка:", err)
		return
	}
	fmt.Fprintln(ui.out, "Сохранено:", p)
}

func (ui *UI) adminExportUser() {
	fmt.Fprint(ui.out, "UserID: ")
	uid := strings.TrimSpace(ui.readLine())
	path := filepath.Join("reports", fmt.Sprintf("user_%s_summary.csv", uid))
	p, err := ui.bank.ExportUserSummaryCSV(domain.UserID(uid), path)
	if err != nil {
		fmt.Fprintln(ui.out, "Ошибка:", err)
		return
	}
	fmt.Fprintln(ui.out, "Сохранено:", p)
}

func (ui *UI) adminListBackups() {
	list, err := ui.bank.ListBackups()
	if err != nil {
		fmt.Fprintln(ui.out, "Ошибка:", err)
		return
	}
	if len(list) == 0 {
		fmt.Fprintln(ui.out, "Нет бэкапов")
		return
	}
	for i, n := range list {
		fmt.Fprintf(ui.out, "%d) %s\n", i+1, n)
	}
}

func (ui *UI) adminRestoreBackup() {
	fmt.Fprint(ui.out, "Имя файла из списка: ")
	name := strings.TrimSpace(ui.readLine())
	if err := ui.bank.RestoreBackup(name); err != nil {
		fmt.Fprintln(ui.out, "Ошибка:", err)
	} else {
		fmt.Fprintln(ui.out, "Восстановлено (перезапустите приложение).")
	}
}
