package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	dataDir       = "data"
	usersFile     = "data/users.json"
	ratesDecimals = 8
	btcRate       = 112937.0

	shopPageSize = 5
	miningWindow = 10 * time.Minute

	startBalanceBTC = 0.0
	startBalanceUSD = 100.0
)

type GPU struct {
	ID    int     `json:"id"`
	Name  string  `json:"name"`
	Rate  float64 `json:"rate"`
	Price float64 `json:"price"`
}

type Business struct {
	ID     int     `json:"id"`
	Name   string  `json:"name"`
	Income float64 `json:"income"`
	Price  float64 `json:"price"`
}

type User struct {
	ID                int64     `json:"id"`
	Username          string    `json:"username"`
	BalanceBTC        float64   `json:"balance_btc"`
	BalanceUSD        float64   `json:"balance_usd"`
	Inventory         []int     `json:"inventory"`
	Businesses        []int     `json:"businesses"`
	CreatedAt         time.Time `json:"created_at"`
	LastAccrualAt     time.Time `json:"last_accrual_at"`
	MiningWindowEnd   time.Time `json:"mining_window_end"`
	LastBonusTime     time.Time `json:"last_bonus_time"`
	FarmCapacity      int       `json:"farm_capacity"`
	LastShopMessageID int       `json:"last_shop_message_id"`
}

type Store struct {
	Users map[int64]*User `json:"users"`
}

var (
	bot        *tgbotapi.BotAPI
	store      Store
	storeMu    sync.RWMutex
	gpuCatalog []GPU
	bizCatalog []Business
	gpuByID    map[int]GPU
	bizByID    map[int]Business
)

func main() {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN is required")
	}
	var err error
	bot, err = tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Authorized on %s", bot.Self.UserName)

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatal(err)
	}
	loadOrInitStore()

	gpuCatalog = buildGPUCatalog()
	bizCatalog = buildBusinessCatalog()

	gpuByID = make(map[int]GPU)
	for _, g := range gpuCatalog {
		gpuByID[g.ID] = g
	}

	bizByID = make(map[int]Business)
	for _, b := range bizCatalog {
		bizByID[b.ID] = b
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message != nil {
			handleMessage(update.Message)
		} else if update.CallbackQuery != nil {
			handleCallback(update.CallbackQuery)
		}
	}
}

func loadOrInitStore() {
	storeMu.Lock()
	defer storeMu.Unlock()
	if _, err := os.Stat(usersFile); errors.Is(err, os.ErrNotExist) {
		store = Store{Users: map[int64]*User{}}
		mustWriteJSON(usersFile, store)
		return
	}
	f, err := os.Open(usersFile)
	if err != nil {
		log.Printf("Error opening users file: %v", err)
		store = Store{Users: map[int64]*User{}}
		return
	}
	defer f.Close()
	if err := json.NewDecoder(f).Decode(&store); err != nil {
		log.Printf("Error decoding users file: %v", err)
		store = Store{Users: map[int64]*User{}}
	}
}

func saveStore() {
	storeMu.Lock()
	defer storeMu.Unlock()
	mustWriteJSON(usersFile, store)
}

func mustWriteJSON(path string, v any) {
	if err := os.MkdirAll("data", 0755); err != nil {
		log.Printf("Error creating data directory: %v", err)
		return
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		log.Printf("Error creating temp file: %v", err)
		return
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		log.Printf("Error encoding JSON: %v", err)
	}
	f.Close()
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("Error renaming temp file: %v", err)
	}
}

func ensureUser(id int64, username string) *User {
	storeMu.Lock()
	defer storeMu.Unlock()
	u, ok := store.Users[id]
	if !ok {
		u = &User{
			ID:                id,
			Username:          username,
			BalanceBTC:        startBalanceBTC,
			BalanceUSD:        startBalanceUSD,
			Inventory:         []int{},
			Businesses:        []int{},
			CreatedAt:         time.Now(),
			LastAccrualAt:     time.Now(),
			LastBonusTime:     time.Now().Add(-25 * time.Hour),
			FarmCapacity:      95,
			LastShopMessageID: 0,
		}
		store.Users[id] = u
	}
	return u
}

func accrueEarnings(u *User) {
	now := time.Now()
	if now.Before(u.MiningWindowEnd) {
		elapsed := now.Sub(u.LastAccrualAt)
		minutes := elapsed.Minutes()

		miningIncome := totalMiningRate(u) * (minutes / 10.0)
		u.BalanceBTC += miningIncome

		businessIncome := totalBusinessIncome(u) * (minutes / 10.0)
		u.BalanceBTC += businessIncome
	}
	u.LastAccrualAt = now
	u.MiningWindowEnd = now.Add(miningWindow)
}

func totalMiningRate(u *User) float64 {
	var rate float64
	for _, id := range u.Inventory {
		if g, ok := gpuByID[id]; ok {
			rate += g.Rate
		}
	}
	return rate
}

func totalBusinessIncome(u *User) float64 {
	var income float64
	for _, id := range u.Businesses {
		if b, ok := bizByID[id]; ok {
			income += b.Income
		}
	}
	return income
}

func handleMessage(m *tgbotapi.Message) {
	u := ensureUser(m.From.ID, m.From.UserName)
	accrueEarnings(u)
	saveStore()

	cmd := m.Text
	if strings.HasPrefix(cmd, "/") {
		parts := strings.Split(cmd, " ")
		switch parts[0] {
		case "/start", "/menu":
			sendMainMenu(u, m.Chat.ID)
		case "/stats":
			sendStats(u, m.Chat.ID)
		case "/ref":
			sendRefInfo(u, m.Chat.ID)
		case "/business":
			sendBusinesses(u, m.Chat.ID)
		case "/btc_buy":
			if len(parts) > 1 {
				amount, _ := strconv.ParseFloat(parts[1], 64)
				buyBTC(u, amount, m.Chat.ID)
			} else {
				sendMessage(m.Chat.ID, "Используйте: /btc_buy [количество]")
			}
		case "/btc_sell":
			if len(parts) > 1 {
				amount, _ := strconv.ParseFloat(parts[1], 64)
				sellBTC(u, amount, m.Chat.ID)
			} else {
				sendMessage(m.Chat.ID, "Используйте: /btc_sell [количество]")
			}
		default:
			sendMainMenu(u, m.Chat.ID)
		}
	} else {
		sendMainMenu(u, m.Chat.ID)
	}
}

func handleCallback(cb *tgbotapi.CallbackQuery) {
	u := ensureUser(cb.From.ID, cb.From.UserName)
	accrueEarnings(u)
	data := cb.Data
	chatID := cb.Message.Chat.ID
	bot.Request(tgbotapi.NewCallback(cb.ID, ""))

	switch {
	case data == "main_menu":
		u.LastShopMessageID = 0
		sendMainMenu(u, chatID)
	case data == "stats":
		u.LastShopMessageID = 0
		sendStats(u, chatID)
	case data == "ref":
		u.LastShopMessageID = 0
		sendRefInfo(u, chatID)
	case data == "business":
		u.LastShopMessageID = 0
		sendBusinesses(u, chatID)
	case data == "farm":
		u.LastShopMessageID = 0
		sendFarm(u, chatID)
	case data == "shop":
		u.LastShopMessageID = 0
		sendShopMenu(u, chatID)
	case data == "gpu_shop":
		sendGPUShop(u, chatID, 1)
	case data == "business_shop":
		sendBusinessShop(u, chatID, 1)
	case data == "daily_bonus":
		u.LastShopMessageID = 0
		claimDailyBonus(u, chatID)
	case data == "convert_btc_usd":
		u.LastShopMessageID = 0
		convertAllBTCtoUSD(u, chatID)
	case strings.HasPrefix(data, "buy_gpu:"):
		id, _ := strconv.Atoi(strings.Split(data, ":")[1])
		buyGPU(u, id, chatID)
	case strings.HasPrefix(data, "buy_biz:"):
		id, _ := strconv.Atoi(strings.Split(data, ":")[1])
		buyBusiness(u, id, chatID)
	case strings.HasPrefix(data, "gpu_shop_page:"):
		page, _ := strconv.Atoi(strings.Split(data, ":")[1])
		sendGPUShop(u, chatID, page)
	case strings.HasPrefix(data, "biz_shop_page:"):
		page, _ := strconv.Atoi(strings.Split(data, ":")[1])
		sendBusinessShop(u, chatID, page)
	}
	saveStore()
}

func sendMainMenu(u *User, chatID int64) {
	currentTime := time.Now().Format("15:04")
	text := fmt.Sprintf("🖥 *Симулятор майнера* 🖥\n\n")
	text += fmt.Sprintf("• Вместимость фермы: %d/95\n", len(u.Inventory))
	text += fmt.Sprintf("• Заработок фермы: %.5f BTC / 10 мин\n", totalMiningRate(u))
	text += fmt.Sprintf("• Доход бизнесов: %.5f BTC / 10 мин\n", totalBusinessIncome(u))
	text += fmt.Sprintf("• Баланс: %.5f BTC\n", u.BalanceBTC)
	text += fmt.Sprintf("• Баланс: %.0f $\n\n", u.BalanceUSD)
	text += fmt.Sprintf("Курс BTC: %.0f $ / 1 BTC\n\n", btcRate)
	text += fmt.Sprintf("%s", currentTime)

	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📊 Личная статистика", "stats"),
			tgbotapi.NewInlineKeyboardButtonData("🎁 Бонусы", "ref"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🏢 Бизнесы", "business"),
			tgbotapi.NewInlineKeyboardButtonData("🖥 Ферма", "farm"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🛒 Магазин", "shop"),
			tgbotapi.NewInlineKeyboardButtonData("🎁 Ежедневный бонус", "daily_bonus"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("💸 Вывести BTC в USD", "convert_btc_usd"),
		),
	)

	sendMessageWithKeyboard(chatID, text, kb)
}

func sendStats(u *User, chatID int64) {
	currentTime := time.Now().Format("15:04")
	text := fmt.Sprintf("📊 *Личная статистика*\n\n")
	text += fmt.Sprintf("• Игрок: @%s\n", u.Username)
	text += fmt.Sprintf("• Видеокарты: %d/95\n", len(u.Inventory))
	text += fmt.Sprintf("• Бизнесы: %d\n", len(u.Businesses))
	text += fmt.Sprintf("• Общий доход: %.5f BTC / 10 мин\n", totalMiningRate(u)+totalBusinessIncome(u))
	text += fmt.Sprintf("• Баланс BTC: %.5f\n", u.BalanceBTC)
	text += fmt.Sprintf("• Баланс USD: %.0f\n", u.BalanceUSD)
	text += fmt.Sprintf("• Играет с: %s\n", u.CreatedAt.Format("02.01.2006"))
	text += fmt.Sprintf("\n%s", currentTime)

	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⬅️ Назад", "main_menu"),
		),
	)

	sendMessageWithKeyboard(chatID, text, kb)
}

func sendRefInfo(u *User, chatID int64) {
	currentTime := time.Now().Format("15:04")
	refLink := fmt.Sprintf("https://t.me/%s?start=ref%d", bot.Self.UserName, u.ID)

	text := fmt.Sprintf("🎁 *Реферальная программа*\n\n")
	text += fmt.Sprintf("Приглашайте друзей и получайте бонусы!\n\n")
	text += fmt.Sprintf("Ваша реферальная ссылка:\n`%s`\n\n", refLink)
	text += fmt.Sprintf("За каждого приглашенного друга вы получите:\n")
	text += fmt.Sprintf("• 1000 $\n")
	text += fmt.Sprintf("• 0.001 BTC\n")
	text += fmt.Sprintf("\n%s", currentTime)

	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⬅️ Назад", "main_menu"),
		),
	)

	sendMessageWithKeyboard(chatID, text, kb)
}

func sendBusinesses(u *User, chatID int64) {
	currentTime := time.Now().Format("15:04")
	text := fmt.Sprintf("🏢 *Ваши бизнесы*\n\n")

	if len(u.Businesses) == 0 {
		text += "У вас пока нет бизнесов. Приобретите их в магазине!\n"
	} else {
		for i, id := range u.Businesses {
			if biz, ok := bizByID[id]; ok {
				text += fmt.Sprintf("%d. %s - %.5f BTC/10мин\n", i+1, biz.Name, biz.Income)
			}
		}
	}

	text += fmt.Sprintf("\nОбщий доход от бизнесов: %.5f BTC/10мин", totalBusinessIncome(u))
	text += fmt.Sprintf("\n\n%s", currentTime)

	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🛒 Магазин бизнесов", "business_shop"),
			tgbotapi.NewInlineKeyboardButtonData("⬅️ Назад", "main_menu"),
		),
	)

	sendMessageWithKeyboard(chatID, text, kb)
}

func sendFarm(u *User, chatID int64) {
	currentTime := time.Now().Format("15:04")
	text := fmt.Sprintf("🖥 *Ваша ферма*\n\n")
	text += fmt.Sprintf("• Вместимость: %d/95\n", len(u.Inventory))
	text += fmt.Sprintf("• Доход фермы: %.5f BTC/10мин\n", totalMiningRate(u))

	if len(u.Inventory) == 0 {
		text += "\nУ вас пока нет видеокарт. Приобретите их в магазине!"
	} else {
		text += "\nУстановленные видеокарты:\n"
		for i, id := range u.Inventory {
			if gpu, ok := gpuByID[id]; ok {
				text += fmt.Sprintf("%d. %s - %.5f BTC/10мин\n", i+1, gpu.Name, gpu.Rate)
			}
		}
	}

	text += fmt.Sprintf("\n%s", currentTime)

	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🛒 Магазин видеокарт", "gpu_shop"),
			tgbotapi.NewInlineKeyboardButtonData("⬅️ Назад", "main_menu"),
		),
	)

	sendMessageWithKeyboard(chatID, text, kb)
}

func sendShopMenu(u *User, chatID int64) {
	currentTime := time.Now().Format("15:04")
	text := "🛒 *Магазин*\n\nВыбери, в какой отдел хочешь пойти:"
	text += fmt.Sprintf("\n\n%s", currentTime)

	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("💻 Видеокарты", "gpu_shop"),
			tgbotapi.NewInlineKeyboardButtonData("🏢 Бизнесы", "business_shop"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⬅️ Назад", "main_menu"),
		),
	)

	sendMessageWithKeyboard(chatID, text, kb)
}

func sendGPUShop(u *User, chatID int64, page int) {
	currentTime := time.Now().Format("15:04")
	start := (page - 1) * shopPageSize
	end := start + shopPageSize
	if end > len(gpuCatalog) {
		end = len(gpuCatalog)
	}

	text := "💻 *Магазин видеокарт*\n\n"
	for _, gpu := range gpuCatalog[start:end] {
		text += fmt.Sprintf("%s - %.0f $\n", gpu.Name, gpu.Price)
		text += fmt.Sprintf("Доход: %.5f BTC/10мин\n\n", gpu.Rate)
	}

	totalPages := (len(gpuCatalog) + shopPageSize - 1) / shopPageSize
	text += fmt.Sprintf("Страница %d/%d\n\n", page, totalPages)
	text += fmt.Sprintf("%s", currentTime)

	kbRows := make([][]tgbotapi.InlineKeyboardButton, 0)

	for _, gpu := range gpuCatalog[start:end] {
		kbRows = append(kbRows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(
				fmt.Sprintf("Купить %s", gpu.Name),
				fmt.Sprintf("buy_gpu:%d", gpu.ID),
			),
		))
	}

	navRow := make([]tgbotapi.InlineKeyboardButton, 0)
	if page > 1 {
		navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData("⬅️", fmt.Sprintf("gpu_shop_page:%d", page-1)))
	}
	if end < len(gpuCatalog) {
		navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData("➡️", fmt.Sprintf("gpu_shop_page:%d", page+1)))
	}
	if len(navRow) > 0 {
		kbRows = append(kbRows, navRow)
	}

	kbRows = append(kbRows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("📌 В главное меню", "main_menu"),
	))

	kb := tgbotapi.NewInlineKeyboardMarkup(kbRows...)

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = kb
	sentMsg, err := bot.Send(msg)
	if err != nil {
		log.Printf("Ошибка отправки магазина GPU: %v", err)
		return
	}

	u.LastShopMessageID = sentMsg.MessageID
}

func sendBusinessShop(u *User, chatID int64, page int) {
	currentTime := time.Now().Format("15:04")
	start := (page - 1) * shopPageSize
	end := start + shopPageSize
	if end > len(bizCatalog) {
		end = len(bizCatalog)
	}

	text := "🏢 *Магазин бизнесов*\n\n"
	for _, biz := range bizCatalog[start:end] {
		text += fmt.Sprintf("%s - %.0f $\n", biz.Name, biz.Price)
		text += fmt.Sprintf("Доход: %.5f BTC/10мин\n\n", biz.Income)
	}

	totalPages := (len(bizCatalog) + shopPageSize - 1) / shopPageSize
	text += fmt.Sprintf("Страница %d/%d\n\n", page, totalPages)
	text += fmt.Sprintf("%s", currentTime)

	kbRows := make([][]tgbotapi.InlineKeyboardButton, 0)

	for _, biz := range bizCatalog[start:end] {
		kbRows = append(kbRows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(
				fmt.Sprintf("Купить %s", biz.Name),
				fmt.Sprintf("buy_biz:%d", biz.ID),
			),
		))
	}

	navRow := make([]tgbotapi.InlineKeyboardButton, 0)
	if page > 1 {
		navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData("⬅️", fmt.Sprintf("biz_shop_page:%d", page-1)))
	}
	if end < len(bizCatalog) {
		navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData("➡️", fmt.Sprintf("biz_shop_page:%d", page+1)))
	}
	if len(navRow) > 0 {
		kbRows = append(kbRows, navRow)
	}

	kbRows = append(kbRows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("📌 В главное меню", "main_menu"),
	))

	kb := tgbotapi.NewInlineKeyboardMarkup(kbRows...)

	if u.LastShopMessageID != 0 {
		editMessage(chatID, u.LastShopMessageID, text, kb)
	} else {

		msg := tgbotapi.NewMessage(chatID, text)
		msg.ParseMode = "Markdown"
		msg.ReplyMarkup = kb
		sentMsg, _ := bot.Send(msg)
		u.LastShopMessageID = sentMsg.MessageID
	}
}

func claimDailyBonus(u *User, chatID int64) {
	currentTime := time.Now().Format("15:04")
	now := time.Now()
	if now.Sub(u.LastBonusTime) < 24*time.Hour {
		timeLeft := 24*time.Hour - now.Sub(u.LastBonusTime)
		text := fmt.Sprintf("🎁 Вы уже получали ежедневный бонус сегодня\n\nСледующий бонус через: %.0f часов\n\n%s", timeLeft.Hours(), currentTime)
		sendMessage(chatID, text)
		return
	}

	bonusBTC := 0.001
	u.BalanceBTC += bonusBTC
	u.LastBonusTime = now

	text := fmt.Sprintf("🎁 *Ежедневный бонус получен!*\n\n+%.5f BTC\n\n%s", bonusBTC, currentTime)
	sendMessage(chatID, text)
}

func convertAllBTCtoUSD(u *User, chatID int64) {
	currentTime := time.Now().Format("15:04")
	if u.BalanceBTC <= 0 {
		sendMessage(chatID, fmt.Sprintf("У вас нет BTC для конвертации\n\n%s", currentTime))
		return
	}

	usdAmount := u.BalanceBTC * btcRate
	u.BalanceUSD += usdAmount
	u.BalanceBTC = 0

	text := fmt.Sprintf("💸 *Конвертация завершена*\n\nВы конвертировали все свои BTC в USD\nПолучено: %.0f $\n\n%s", usdAmount, currentTime)
	sendMessage(chatID, text)
}

func buyGPU(u *User, id int, chatID int64) {
	currentTime := time.Now().Format("15:04")
	gpu, exists := gpuByID[id]
	if !exists {
		sendMessage(chatID, fmt.Sprintf("Эта видеокарта не найдена\n\n%s", currentTime))
		return
	}

	if u.BalanceUSD < gpu.Price {
		sendMessage(chatID, fmt.Sprintf("Недостаточно средств для покупки\n\n%s", currentTime))
		return
	}

	if u.FarmCapacity == 0 {
		u.FarmCapacity = 95
	}

	if len(u.Inventory) >= u.FarmCapacity {
		sendMessage(chatID, fmt.Sprintf("Достигнут лимит фермы. Нельзя купить больше видеокарт\n\n%s", currentTime))
		return
	}

	u.BalanceUSD -= gpu.Price
	u.Inventory = append(u.Inventory, id)

	text := fmt.Sprintf("✅ *Покупка совершена*\n\nВы приобрели: %s\nПотрачено: %.0f $\nДоход: %.5f BTC/10мин\n\n%s",
		gpu.Name, gpu.Price, gpu.Rate, currentTime)
	sendMessage(chatID, text)

	sendGPUShop(u, chatID, 1)
}

func buyBusiness(u *User, id int, chatID int64) {
	currentTime := time.Now().Format("15:04")
	biz, exists := bizByID[id]
	if !exists {
		sendMessage(chatID, fmt.Sprintf("Этот бизнес не найден\n\n%s", currentTime))
		return
	}

	if u.BalanceUSD < biz.Price {
		sendMessage(chatID, fmt.Sprintf("Недостаточно средств для покупки\n\n%s", currentTime))
		return
	}

	for _, bizID := range u.Businesses {
		if bizID == id {
			sendMessage(chatID, fmt.Sprintf("У вас уже есть этот бизнес\n\n%s", currentTime))
			return
		}
	}

	u.BalanceUSD -= biz.Price
	u.Businesses = append(u.Businesses, id)

	text := fmt.Sprintf("✅ *Покупка совершена*\n\nВы приобрели: %s\nПотрачено: %.0f $\nДоход: %.5f BTC/10мин\n\n%s",
		biz.Name, biz.Price, biz.Income, currentTime)
	sendMessage(chatID, text)

	sendBusinessShop(u, chatID, 1)
}

func buyBTC(u *User, amount float64, chatID int64) {
	currentTime := time.Now().Format("15:04")
	cost := amount * btcRate
	if u.BalanceUSD < cost {
		sendMessage(chatID, fmt.Sprintf("Недостаточно USD для покупки BTC\n\n%s", currentTime))
		return
	}

	u.BalanceUSD -= cost
	u.BalanceBTC += amount

	text := fmt.Sprintf("✅ *Покупка BTC совершена*\n\nКуплено: %.5f BTC\nПотрачено: %.0f $\n\n%s", amount, cost, currentTime)
	sendMessage(chatID, text)
}

func sellBTC(u *User, amount float64, chatID int64) {
	currentTime := time.Now().Format("15:04")
	if u.BalanceBTC < amount {
		sendMessage(chatID, fmt.Sprintf("Недостаточно BTC для продажи\n\n%s", currentTime))
		return
	}

	income := amount * btcRate
	u.BalanceBTC -= amount
	u.BalanceUSD += income

	text := fmt.Sprintf("✅ *Продажа BTC совершена*\n\nПродано: %.5f BTC\nПолучено: %.0f $\n\n%s", amount, income, currentTime)
	sendMessage(chatID, text)
}

func sendMessage(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	bot.Send(msg)
}

func sendMessageWithKeyboard(chatID int64, text string, kb tgbotapi.InlineKeyboardMarkup) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = kb
	bot.Send(msg)
}

func editMessage(chatID int64, messageID int, text string, kb tgbotapi.InlineKeyboardMarkup) {
	msg := tgbotapi.NewEditMessageTextAndMarkup(chatID, messageID, text, kb)
	msg.ParseMode = "Markdown"
	bot.Send(msg)
}

func buildGPUCatalog() []GPU {
	return []GPU{
		{1, "GeForce GT 710 1GB", 0.0000010, 50},
		{2, "GeForce GT 730 2GB", 0.0000018, 90},
		{3, "GeForce GTX 750 Ti", 0.0000035, 150},
		{4, "GeForce GTX 950", 0.0000070, 300},
		{5, "GeForce GTX 960", 0.0000120, 500},
		{6, "GeForce GTX 970", 0.0000200, 800},
		{7, "GeForce GTX 980", 0.0000300, 1200},
		{8, "GeForce GTX 1050 Ti", 0.0000450, 1800},
		{9, "GeForce GTX 1060 3GB", 0.0000700, 2800},
		{10, "GeForce GTX 1060 6GB", 0.0000900, 3600},
		{11, "GeForce GTX 1070", 0.0001300, 5200},
		{12, "GeForce GTX 1070 Ti", 0.0001500, 6000},
		{13, "GeForce GTX 1080", 0.0001800, 7200},
		{14, "GeForce GTX 1080 Ti", 0.0002500, 10000},
		{15, "GeForce RTX 2060", 0.0003000, 12000},
		{16, "GeForce RTX 2060 Super", 0.0003500, 14000},
		{17, "GeForce RTX 2070", 0.0004000, 16000},
		{18, "GeForce RTX 2070 Super", 0.0004500, 18000},
		{19, "GeForce RTX 2080", 0.0005000, 20000},
		{20, "GeForce RTX 2080 Super", 0.0005500, 22000},
		{21, "GeForce RTX 2080 Ti", 0.0007000, 28000},
		{22, "GeForce RTX 3050", 0.0008000, 32000},
		{23, "GeForce RTX 3060", 0.0010000, 40000},
		{24, "GeForce RTX 3060 Ti", 0.0012000, 48000},
		{25, "GeForce RTX 3070", 0.0015000, 60000},
		{26, "GeForce RTX 3070 Ti", 0.0017000, 68000},
		{27, "GeForce RTX 3080 10GB", 0.0020000, 80000},
		{28, "GeForce RTX 3080 12GB", 0.0022000, 88000},
		{29, "GeForce RTX 3080 Ti", 0.0025000, 100000},
		{30, "GeForce RTX 3090", 0.0030000, 120000},
		{31, "GeForce RTX 3090 Ti", 0.0035000, 140000},
		{32, "GeForce RTX 4060", 0.0040000, 160000},
		{33, "GeForce RTX 4060 Ti", 0.0045000, 180000},
		{34, "GeForce RTX 4070", 0.0050000, 200000},
		{35, "GeForce RTX 4070 Ti", 0.0060000, 240000},
		{36, "GeForce RTX 4080", 0.0075000, 300000},
		{37, "GeForce RTX 4080 Super", 0.0080000, 320000},
		{38, "GeForce RTX 4090", 0.0100000, 400000},
		{39, "GeForce RTX 4090 Ti", 0.0120000, 480000},
		{40, "Radeon RX 460", 0.0000050, 200},
		{41, "Radeon RX 470", 0.0000150, 600},
		{42, "Radeon RX 480", 0.0000250, 1000},
		{43, "Radeon RX 550", 0.0000080, 320},
		{44, "Radeon RX 560", 0.0000120, 480},
		{45, "Radeon RX 570", 0.0000300, 1200},
		{46, "Radeon RX 580", 0.0000450, 1800},
		{47, "Radeon RX 590", 0.0000600, 2400},
		{48, "Radeon RX Vega 56", 0.0001000, 4000},
		{49, "Radeon RX Vega 64", 0.0001300, 5200},
		{50, "Radeon VII", 0.0002000, 8000},
		{51, "Radeon RX 5500 XT", 0.0002500, 10000},
		{52, "Radeon RX 5600 XT", 0.0003000, 12000},
		{53, "Radeon RX 5700", 0.0003500, 14000},
		{54, "Radeon RX 5700 XT", 0.0004000, 16000},
		{55, "Radeon RX 6600", 0.0005000, 20000},
		{56, "Radeon RX 6600 XT", 0.0006000, 24000},
		{57, "Radeon RX 6700 XT", 0.0008000, 32000},
		{58, "Radeon RX 6800", 0.0010000, 40000},
		{59, "Radeon RX 6800 XT", 0.0012000, 48000},
		{60, "Radeon RX 6900 XT", 0.0015000, 60000},
	}
}

func buildBusinessCatalog() []Business {
	return []Business{
		{1, "Небольшая ферма", 0.005, 5000},
		{2, "Средняя ферма", 0.015, 15000},
		{3, "Крупная ферма", 0.030, 30000},
		{4, "Криптообменник", 0.050, 50000},
		{5, "Майнинг-отель", 0.100, 100000},
		{6, "Криптофонд", 0.200, 200000},
		{7, "Блокчейн стартап", 0.500, 500000},
		{8, "Криптобиржа", 1.000, 1000000},
		{9, "Международная майнинговая компания", 2.000, 2000000},
		{10, "Глобальный блокчейн-холдинг", 5.000, 5000000},
	}
}
