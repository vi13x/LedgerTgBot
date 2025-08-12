package main

import (
	"log"
	"os"

	"bank-bot/bot"
	"bank-bot/data"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func main() {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("Не задан TELEGRAM_BOT_TOKEN")
	}

	botAPI, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatal(err)
	}

	bank, err := data.InitStorage("data.json")
	if err != nil {
		log.Fatal(err)
	}

	botAPI.Debug = true
	log.Printf("Запущен бот: %s", botAPI.Self.UserName)

	bot.Start(botAPI, bank)
}
