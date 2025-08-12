package bot

import (
	"fmt"
	"log"
	"strconv"

	"bank-bot/data"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func Start(bot *tgbotapi.BotAPI, bank *data.Bank) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		if update.Message.IsCommand() {
			handleCommand(bot, bank, update.Message)
		} else {
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "ℹ️ Используйте /help для списка команд")
			bot.Send(msg)
		}
	}
}

func handleCommand(bot *tgbotapi.BotAPI, bank *data.Bank, msg *tgbotapi.Message) {
	response := tgbotapi.NewMessage(msg.Chat.ID, "")

	switch msg.Command() {
	case "start", "help":
		response.Text = `🏦 Доступные команды:
/create_account <имя>
/deposit <ID> <сумма>
/transfer <от_ID> <на_ID> <сумма>
/balance <ID>
/history <ID>`
	case "create_account":
		args := msg.CommandArguments()
		if args == "" {
			response.Text = "Формат: /create_account Имя"
		} else {
			acc, err := bank.CreateAccount(args)
			if err != nil {
				response.Text = "❌ " + err.Error()
			} else {
				response.Text = fmt.Sprintf("✅ Создан аккаунт %s (ID %d)", acc.Name, acc.ID)
			}
		}
	case "deposit":
		var id int
		var amount float64
		if _, err := fmt.Sscanf(msg.CommandArguments(), "%d %f", &id, &amount); err != nil {
			response.Text = "Формат: /deposit ID Сумма"
		} else {
			err := bank.Deposit(id, amount)
			if err != nil {
				response.Text = "❌ " + err.Error()
			} else {
				response.Text = fmt.Sprintf("✅ Счёт %d пополнен на %.2f", id, amount)
			}
		}
	case "transfer":
		var from, to int
		var amount float64
		if _, err := fmt.Sscanf(msg.CommandArguments(), "%d %d %f", &from, &to, &amount); err != nil {
			response.Text = "Формат: /transfer От_ID На_ID Сумма"
		} else {
			err := bank.Transfer(from, to, amount)
			if err != nil {
				response.Text = "❌ " + err.Error()
			} else {
				response.Text = fmt.Sprintf("✅ Переведено %.2f со счёта %d на %d", amount, from, to)
			}
		}
	case "balance":
		id, err := strconv.Atoi(msg.CommandArguments())
		if err != nil {
			response.Text = "Формат: /balance ID"
		} else {
			acc, err := bank.GetAccount(id)
			if err != nil {
				response.Text = "❌ " + err.Error()
			} else {
				response.Text = fmt.Sprintf("💰 Баланс %s (ID %d): %.2f", acc.Name, acc.ID, acc.Balance)
			}
		}
	case "history":
		id, err := strconv.Atoi(msg.CommandArguments())
		if err != nil {
			response.Text = "Формат: /history ID"
		} else {
			history, _ := bank.GetTransactionHistory(id)
			if len(history) == 0 {
				response.Text = "📭 История пуста"
			} else {
				txt := "📜 Последние операции:\n"
				for _, t := range history {
					from := strconv.Itoa(t.From)
					if t.From == 0 {
						from = "Пополнение"
					}
					txt += fmt.Sprintf("%s: %.2f (с %s на %d)\n",
						t.Timestamp.Format("02.01.2006 15:04"), t.Amount, from, t.To)
				}
				response.Text = txt
			}
		}
	default:
		response.Text = "Неизвестная команда"
	}

	if _, err := bot.Send(response); err != nil {
		log.Println("Ошибка отправки:", err)
	}
}
