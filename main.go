package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Константы состояний
const (
	StateMainMenu = iota
	StateWaitingAccountName
	StateWaitingDepositAccount
	StateWaitingDepositAmount
	StateWaitingTransferFrom
	StateWaitingTransferTo
	StateWaitingTransferAmount
	StateWaitingHistoryAccount
)

// Структуры данных
type UserState struct {
	State       int
	TempAccount *Account
	TempData    map[string]interface{}
}

type Account struct {
	ID      int     `json:"id"`
	Name    string  `json:"name"`
	Balance float64 `json:"balance"`
}

type Transaction struct {
	ID        int       `json:"id"`
	From      int       `json:"from"`
	To        int       `json:"to"`
	Amount    float64   `json:"amount"`
	Timestamp time.Time `json:"timestamp"`
}

type Bank struct {
	Accounts      map[int]*Account `json:"accounts"`
	Transactions  []*Transaction   `json:"transactions"`
	mu            sync.Mutex
	nextAccountID int `json:"next_account_id"`
	userStates    map[int64]*UserState
}

var (
	bot      *tgbotapi.BotAPI
	bank     *Bank
	dataFile = "bank_data.json"
)

func main() {
	// Инициализация бота
	var err error
	bot, err = tgbotapi.NewBotAPI(os.Getenv("TELEGRAM_BOT_TOKEN"))
	if err != nil {
		log.Panicf("Ошибка инициализации бота: %v", err)
	}

	bot.Debug = true
	log.Printf("Банковский бот запущен: %s", bot.Self.UserName)

	// Инициализация банка
	bank = &Bank{
		Accounts:   make(map[int]*Account),
		userStates: make(map[int64]*UserState),
	}

	// Загрузка данных
	if err := bank.LoadFromFile(dataFile); err != nil {
		log.Printf("Не удалось загрузить данные: %v", err)
	}

	// Настройка обновлений
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("Сохранение данных и выключение...")
		if err := bank.SaveToFile(dataFile); err != nil {
			log.Printf("Ошибка сохранения: %v", err)
		}
		bot.StopReceivingUpdates()
		os.Exit(0)
	}()

	// Обработка сообщений
	for update := range updates {
		if update.Message == nil {
			continue
		}

		// Инициализация состояния пользователя
		if _, ok := bank.userStates[update.Message.Chat.ID]; !ok {
			bank.userStates[update.Message.Chat.ID] = &UserState{
				State:    StateMainMenu,
				TempData: make(map[string]interface{}),
			}
		}

		userState := bank.userStates[update.Message.Chat.ID]

		if update.Message.IsCommand() {
			handleCommand(update.Message, userState)
		} else {
			handleTextMessage(update.Message, userState)
		}
	}
}

// Методы Bank
func (b *Bank) CreateAccount(name string) *Account {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.nextAccountID++
	acc := &Account{
		ID:      b.nextAccountID,
		Name:    name,
		Balance: 0,
	}
	b.Accounts[acc.ID] = acc
	return acc
}

func (b *Bank) Deposit(id int, amount float64) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	acc, ok := b.Accounts[id]
	if !ok {
		return fmt.Errorf("аккаунт %d не найден", id)
	}

	acc.Balance += amount
	b.recordTransaction(0, id, amount)
	return nil
}

func (b *Bank) Transfer(from, to int, amount float64) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	sender, ok := b.Accounts[from]
	if !ok {
		return fmt.Errorf("отправитель %d не найден", from)
	}

	receiver, ok := b.Accounts[to]
	if !ok {
		return fmt.Errorf("получатель %d не найден", to)
	}

	if sender.Balance < amount {
		return fmt.Errorf("недостаточно средств")
	}

	sender.Balance -= amount
	receiver.Balance += amount
	b.recordTransaction(from, to, amount)
	return nil
}

func (b *Bank) GetAccount(id int) (*Account, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	acc, ok := b.Accounts[id]
	if !ok {
		return nil, fmt.Errorf("аккаунт %d не найден", id)
	}
	return acc, nil
}

func (b *Bank) GetTransactionHistory(id int) []*Transaction {
	b.mu.Lock()
	defer b.mu.Unlock()

	var result []*Transaction
	for _, t := range b.Transactions {
		if t.From == id || t.To == id {
			result = append(result, t)
		}
	}
	return result
}

func (b *Bank) recordTransaction(from, to int, amount float64) {
	t := &Transaction{
		ID:        len(b.Transactions) + 1,
		From:      from,
		To:        to,
		Amount:    amount,
		Timestamp: time.Now(),
	}
	b.Transactions = append(b.Transactions, t)
}

func (b *Bank) SaveToFile(filename string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filename, data, 0644)
}

func (b *Bank) LoadFromFile(filename string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	data, err := os.ReadFile(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal(data, b)
}

// Обработчики сообщений
func handleCommand(msg *tgbotapi.Message, userState *UserState) {
	switch msg.Command() {
	case "start":
		userState.State = StateMainMenu
		sendMainMenu(msg.Chat.ID)
	case "help":
		sendHelpMessage(msg.Chat.ID)
	default:
		sendMessage(msg.Chat.ID, "Неизвестная команда. Используйте /help для справки")
	}
}

func handleTextMessage(msg *tgbotapi.Message, userState *UserState) {
	switch userState.State {
	case StateMainMenu:
		handleMainMenu(msg, userState)
	case StateWaitingAccountName:
		handleAccountCreation(msg, userState)
	case StateWaitingDepositAccount:
		handleDepositAccountSelect(msg, userState)
	case StateWaitingDepositAmount:
		handleDepositAmount(msg, userState)
	case StateWaitingTransferFrom:
		handleTransferFromSelect(msg, userState)
	case StateWaitingTransferTo:
		handleTransferToSelect(msg, userState)
	case StateWaitingTransferAmount:
		handleTransferAmount(msg, userState)
	case StateWaitingHistoryAccount:
		handleHistoryAccountSelect(msg, userState)
	default:
		sendMainMenu(msg.Chat.ID)
	}
}

func handleMainMenu(msg *tgbotapi.Message, userState *UserState) {
	text := strings.ToLower(msg.Text)
	switch {
	case strings.Contains(text, "создать счет"):
		userState.State = StateWaitingAccountName
		sendMessage(msg.Chat.ID, "Введите имя для нового счета:")
	case strings.Contains(text, "пополнить счет"):
		userState.State = StateWaitingDepositAccount
		sendAccountsList(msg.Chat.ID, "Выберите счет для пополнения:")
	case strings.Contains(text, "перевод"):
		userState.State = StateWaitingTransferFrom
		sendAccountsList(msg.Chat.ID, "Выберите счет для перевода с:")
	case strings.Contains(text, "мои счета"):
		showAccountsList(msg.Chat.ID)
	case strings.Contains(text, "история операций"):
		userState.State = StateWaitingHistoryAccount
		sendAccountsList(msg.Chat.ID, "Выберите счет для просмотра истории:")
	case strings.Contains(text, "цели накоплений"):
		sendMessage(msg.Chat.ID, "Функция в разработке")
	default:
		sendMainMenu(msg.Chat.ID)
	}
}

func handleAccountCreation(msg *tgbotapi.Message, userState *UserState) {
	if msg.Text == "" {
		sendMessage(msg.Chat.ID, "Имя счета не может быть пустым")
		return
	}

	account := bank.CreateAccount(msg.Text)
	userState.State = StateMainMenu
	sendMessage(msg.Chat.ID, fmt.Sprintf("✅ Счет '%s' создан! ID: %d", account.Name, account.ID))
	sendMainMenu(msg.Chat.ID)
}

func handleDepositAccountSelect(msg *tgbotapi.Message, userState *UserState) {
	accountID, err := extractAccountID(msg.Text)
	if err != nil {
		sendMessage(msg.Chat.ID, "Неверный формат счета")
		sendAccountsList(msg.Chat.ID, "Выберите счет для пополнения:")
		return
	}

	userState.TempData["deposit_account"] = accountID
	userState.State = StateWaitingDepositAmount
	sendMessage(msg.Chat.ID, "Введите сумму для пополнения:")
}

func handleDepositAmount(msg *tgbotapi.Message, userState *UserState) {
	amount, err := strconv.ParseFloat(msg.Text, 64)
	if err != nil || amount <= 0 {
		sendMessage(msg.Chat.ID, "Неверная сумма. Введите положительное число:")
		return
	}

	accountID := userState.TempData["deposit_account"].(int)
	err = bank.Deposit(accountID, amount)
	if err != nil {
		sendMessage(msg.Chat.ID, fmt.Sprintf("Ошибка: %v", err))
	} else {
		sendMessage(msg.Chat.ID, fmt.Sprintf("✅ Счет #%d пополнен на %.2f", accountID, amount))
	}

	userState.State = StateMainMenu
	delete(userState.TempData, "deposit_account")
	sendMainMenu(msg.Chat.ID)
}

func handleTransferFromSelect(msg *tgbotapi.Message, userState *UserState) {
	accountID, err := extractAccountID(msg.Text)
	if err != nil {
		sendMessage(msg.Chat.ID, "Неверный формат счета")
		sendAccountsList(msg.Chat.ID, "Выберите счет для перевода с:")
		return
	}

	userState.TempData["transfer_from"] = accountID
	userState.State = StateWaitingTransferTo
	sendAccountsList(msg.Chat.ID, "Выберите счет для перевода на:")
}

func handleTransferToSelect(msg *tgbotapi.Message, userState *UserState) {
	accountID, err := extractAccountID(msg.Text)
	if err != nil {
		sendMessage(msg.Chat.ID, "Неверный формат счета")
		sendAccountsList(msg.Chat.ID, "Выберите счет для перевода на:")
		return
	}

	fromAccount := userState.TempData["transfer_from"].(int)
	if accountID == fromAccount {
		sendMessage(msg.Chat.ID, "Нельзя переводить на тот же счет")
		sendAccountsList(msg.Chat.ID, "Выберите другой счет для перевода на:")
		return
	}

	userState.TempData["transfer_to"] = accountID
	userState.State = StateWaitingTransferAmount
	sendMessage(msg.Chat.ID, "Введите сумму для перевода:")
}

func handleTransferAmount(msg *tgbotapi.Message, userState *UserState) {
	amount, err := strconv.ParseFloat(msg.Text, 64)
	if err != nil || amount <= 0 {
		sendMessage(msg.Chat.ID, "Неверная сумма. Введите положительное число:")
		return
	}

	fromAccount := userState.TempData["transfer_from"].(int)
	toAccount := userState.TempData["transfer_to"].(int)

	err = bank.Transfer(fromAccount, toAccount, amount)
	if err != nil {
		sendMessage(msg.Chat.ID, fmt.Sprintf("Ошибка: %v", err))
	} else {
		sendMessage(msg.Chat.ID, fmt.Sprintf("✅ Перевод %.2f с #%d на #%d выполнен", amount, fromAccount, toAccount))
	}

	userState.State = StateMainMenu
	delete(userState.TempData, "transfer_from")
	delete(userState.TempData, "transfer_to")
	sendMainMenu(msg.Chat.ID)
}

func handleHistoryAccountSelect(msg *tgbotapi.Message, userState *UserState) {
	accountID, err := extractAccountID(msg.Text)
	if err != nil {
		sendMessage(msg.Chat.ID, "Неверный формат счета")
		sendAccountsList(msg.Chat.ID, "Выберите счет для просмотра истории:")
		return
	}

	history := bank.GetTransactionHistory(accountID)
	if len(history) == 0 {
		sendMessage(msg.Chat.ID, fmt.Sprintf("История операций по счету #%d пуста", accountID))
	} else {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("📜 История операций по счету #%d:\n\n", accountID))
		for _, t := range history {
			from := "Пополнение"
			if t.From != 0 {
				from = fmt.Sprintf("#%d", t.From)
			}
			sb.WriteString(fmt.Sprintf("▫️ %s: %.2f (с %s → на #%d)\n",
				t.Timestamp.Format("02.01 15:04"), t.Amount, from, t.To))
		}
		sendMessage(msg.Chat.ID, sb.String())
	}

	userState.State = StateMainMenu
	sendMainMenu(msg.Chat.ID)
}

// Вспомогательные функции
func extractAccountID(text string) (int, error) {
	if len(text) == 0 || text[0] != '#' {
		return 0, fmt.Errorf("invalid format")
	}
	return strconv.Atoi(text[1:strings.Index(text, " ")])
}

func sendMainMenu(chatID int64) {
	msg := tgbotapi.NewMessage(chatID, "🏦 Главное меню банковского бота")
	msg.ReplyMarkup = tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("Создать счет"),
			tgbotapi.NewKeyboardButton("Мои счета"),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("Пополнить счет"),
			tgbotapi.NewKeyboardButton("Перевод"),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("История операций"),
			tgbotapi.NewKeyboardButton("Цели накоплений"),
		),
	)
	bot.Send(msg)
}

func sendHelpMessage(chatID int64) {
	text := `📚 Справка по банковскому боту:

*Основные команды:*
- Создать счет: регистрация нового банковского счета
- Мои счета: просмотр всех ваших счетов и балансов
- Пополнить счет: внесение средств на выбранный счет
- Перевод: перевод между своими счетами
- История операций: просмотр всех транзакций
- Цели накоплений: управление финансовыми целями

Для начала работы нажмите "Создать счет" или выберите нужную опцию из меню.`
	sendMessage(chatID, text)
}

func sendMessage(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	bot.Send(msg)
}

func sendAccountsList(chatID int64, message string) {
	if len(bank.Accounts) == 0 {
		sendMessage(chatID, "У вас пока нет счетов")
		sendMainMenu(chatID)
		return
	}

	var buttons [][]tgbotapi.KeyboardButton
	for _, acc := range bank.Accounts {
		btn := tgbotapi.NewKeyboardButton(fmt.Sprintf("#%d %s", acc.ID, acc.Name))
		buttons = append(buttons, tgbotapi.NewKeyboardButtonRow(btn))
	}

	// Добавляем кнопку отмены
	buttons = append(buttons, tgbotapi.NewKeyboardButtonRow(
		tgbotapi.NewKeyboardButton("Отмена"),
	))

	msg := tgbotapi.NewMessage(chatID, message)
	msg.ReplyMarkup = tgbotapi.NewReplyKeyboard(buttons...)
	bot.Send(msg)
}

func showAccountsList(chatID int64) {
	if len(bank.Accounts) == 0 {
		sendMessage(chatID, "У вас пока нет счетов")
		sendMainMenu(chatID)
		return
	}

	var sb strings.Builder
	sb.WriteString("📋 Ваши счета:\n\n")
	for _, acc := range bank.Accounts {
		sb.WriteString(fmt.Sprintf("🔹 #%d: %s - %.2f руб.\n", acc.ID, acc.Name, acc.Balance))
	}

	sendMessage(chatID, sb.String())
	sendMainMenu(chatID)
}
