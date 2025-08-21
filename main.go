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

	shopPageSize = 5
	miningWindow = 3 * time.Hour

	startBalance = 100.0
	currency     = "USDT"
)

type GPU struct {
	ID    int     `json:"id"`
	Name  string  `json:"name"`
	Rate  float64 `json:"rate"`
	Price float64 `json:"price"`
}

type User struct {
	ID              int64     `json:"id"`
	Username        string    `json:"username"`
	Balance         float64   `json:"balance"`
	Inventory       []int     `json:"inventory"`
	CreatedAt       time.Time `json:"created_at"`
	LastAccrualAt   time.Time `json:"last_accrual_at"`
	MiningWindowEnd time.Time `json:"mining_window_end"`
	LastBotMsgID    int       `json:"last_bot_msg_id"`
}

type Store struct {
	Users map[int64]*User `json:"users"`
}

var (
	bot         *tgbotapi.BotAPI
	store       Store
	storeMu     sync.RWMutex
	catalog     []GPU
	catalogByID map[int]GPU
)

// --- main ---

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

	catalog = buildCatalog()
	catalogByID = map[int]GPU{}
	for _, g := range catalog {
		catalogByID[g.ID] = g
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

// --- Persistence ---

func loadOrInitStore() {
	storeMu.Lock()
	defer storeMu.Unlock()
	if _, err := os.Stat(usersFile); errors.Is(err, os.ErrNotExist) {
		store = Store{Users: map[int64]*User{}}
		mustWriteJSON(usersFile, store)
		return
	}
	f, _ := os.Open(usersFile)
	defer f.Close()
	json.NewDecoder(f).Decode(&store)
}

func saveStore() {
	storeMu.Lock()
	defer storeMu.Unlock()
	mustWriteJSON(usersFile, store)
}

func mustWriteJSON(path string, v any) {
	tmp := path + ".tmp"
	f, _ := os.Create(tmp)
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.Encode(v)
	f.Close()
	os.Rename(tmp, path)
}

// --- User logic ---

func ensureUser(id int64, username string) *User {
	storeMu.Lock()
	defer storeMu.Unlock()
	u, ok := store.Users[id]
	if !ok {
		u = &User{ID: id, Username: username, Balance: startBalance,
			CreatedAt: time.Now(), LastAccrualAt: time.Now(),
			MiningWindowEnd: time.Now().Add(miningWindow)}
		store.Users[id] = u
	}
	return u
}

func accrueOnInteraction(u *User) {
	now := time.Now()
	accrualEnd := u.MiningWindowEnd
	if now.Before(accrualEnd) {
		accrualEnd = now
	}
	if accrualEnd.After(u.LastAccrualAt) {
		sec := accrualEnd.Sub(u.LastAccrualAt).Seconds()
		u.Balance += totalRate(u) * sec
	}
	u.LastAccrualAt = now
	u.MiningWindowEnd = now.Add(miningWindow)
}

func totalRate(u *User) float64 {
	var r float64
	for _, id := range u.Inventory {
		if g, ok := catalogByID[id]; ok {
			r += g.Rate
		}
	}
	return r
}

// --- Handlers ---

func handleMessage(m *tgbotapi.Message) {
	u := ensureUser(m.From.ID, m.From.UserName)
	accrueOnInteraction(u)
	saveStore()
	if m.Text == "/start" {
		showMainMenu(u, m.Chat.ID)
	} else {
		showMainMenu(u, m.Chat.ID)
	}
}

func handleCallback(cb *tgbotapi.CallbackQuery) {
	u := ensureUser(cb.From.ID, cb.From.UserName)
	accrueOnInteraction(u)
	data := cb.Data
	chatID := cb.Message.Chat.ID
	bot.Request(tgbotapi.NewCallback(cb.ID, ""))

	switch {
	case data == "menu":
		showMainMenu(u, chatID)
	case data == "balance":
		showBalance(u, chatID)
	case data == "mine":
		showMine(u, chatID)
	case strings.HasPrefix(data, "shop"):
		page := 1
		if strings.Contains(data, ":") {
			p, _ := strconv.Atoi(strings.Split(data, ":")[1])
			page = p
		}
		showShop(u, chatID, page)
	case strings.HasPrefix(data, "buy"):
		id, _ := strconv.Atoi(strings.Split(data, ":")[1])
		buyGPU(u, id)
		showInventory(u, chatID)
	case data == "inventory":
		showInventory(u, chatID)
	case strings.HasPrefix(data, "sell"):
		id, _ := strconv.Atoi(strings.Split(data, ":")[1])
		sellGPU(u, id)
		showInventory(u, chatID)
	case data == "reset":
		u.Balance = startBalance
		u.Inventory = []int{}
		showMainMenu(u, chatID)
	}
	saveStore()
}

// --- UI Screens ---

func showMainMenu(u *User, chatID int64) {
	text := fmt.Sprintf("Баланс: *%s %s*\nСкорость: *%s %s/сек*", fmtAmt(u.Balance), currency, fmtAmt(totalRate(u)), currency)
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("💰 Баланс", "balance"),
			tgbotapi.NewInlineKeyboardButtonData("⛏ Добыча", "mine")),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🛒 Магазин", "shop:1"),
			tgbotapi.NewInlineKeyboardButtonData("🎒 Инвентарь", "inventory")),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("♻ Сброс", "reset")))
	sendOrReplace(u, chatID, text, &kb)
}

func showBalance(u *User, chatID int64) {
	text := fmt.Sprintf("Ваш баланс: *%s %s*", fmtAmt(u.Balance), currency)
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⬅ Назад", "menu")))
	sendOrReplace(u, chatID, text, &kb)
}

func showMine(u *User, chatID int64) {
	text := fmt.Sprintf("⛏ Ваша добыча активна! Скорость: *%s %s/сек*", fmtAmt(totalRate(u)), currency)
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⬅ Назад", "menu")))
	sendOrReplace(u, chatID, text, &kb)
}

func showShop(u *User, chatID int64, page int) {
	start := (page - 1) * shopPageSize
	end := start + shopPageSize
	if end > len(catalog) {
		end = len(catalog)
	}
	text := "🛒 Магазин видеокарт:\n"
	rows := [][]tgbotapi.InlineKeyboardButton{}
	for _, g := range catalog[start:end] {
		text += fmt.Sprintf("%d. %s — %s %s (⛏ %s/сек)\n", g.ID, g.Name, fmtAmt(g.Price), currency, fmtAmt(g.Rate))
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Купить "+strconv.Itoa(g.ID), fmt.Sprintf("buy:%d", g.ID))))
	}
	nav := []tgbotapi.InlineKeyboardButton{}
	if start > 0 {
		nav = append(nav, tgbotapi.NewInlineKeyboardButtonData("⬅", fmt.Sprintf("shop:%d", page-1)))
	}
	if end < len(catalog) {
		nav = append(nav, tgbotapi.NewInlineKeyboardButtonData("➡", fmt.Sprintf("shop:%d", page+1)))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⬅ Назад", "menu")))
	kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
	sendOrReplace(u, chatID, text, &kb)
}

func showInventory(u *User, chatID int64) {
	if len(u.Inventory) == 0 {
		sendOrReplace(u, chatID, "🎒 Ваш инвентарь пуст.", &tgbotapi.InlineKeyboardMarkup{
			InlineKeyboard: [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("⬅ Назад", "menu")}},
		})
		return
	}
	text := "🎒 Ваши видеокарты:\n"
	rows := [][]tgbotapi.InlineKeyboardButton{}
	for _, id := range u.Inventory {
		g := catalogByID[id]
		text += fmt.Sprintf("- %s (⛏ %s/сек)\n", g.Name, fmtAmt(g.Rate))
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Продать "+strconv.Itoa(g.ID), fmt.Sprintf("sell:%d", g.ID))))
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⬅ Назад", "menu")))
	kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
	sendOrReplace(u, chatID, text, &kb)
}

// --- Game actions ---

func buyGPU(u *User, id int) {
	g, ok := catalogByID[id]
	if !ok || u.Balance < g.Price {
		return
	}
	u.Balance -= g.Price
	u.Inventory = append(u.Inventory, id)
}

func sellGPU(u *User, id int) {
	g, ok := catalogByID[id]
	if !ok {
		return
	}
	for i, x := range u.Inventory {
		if x == id {
			u.Inventory = append(u.Inventory[:i], u.Inventory[i+1:]...)
			u.Balance += g.Price * 0.5
			return
		}
	}
}

// --- Helpers ---

func sendOrReplace(u *User, chatID int64, text string, kb *tgbotapi.InlineKeyboardMarkup) {
	if u.LastBotMsgID != 0 {
		edit := tgbotapi.NewEditMessageTextAndMarkup(chatID, u.LastBotMsgID, text, *kb)
		edit.ParseMode = "Markdown"
		if _, err := bot.Request(edit); err == nil {
			return
		}
	}
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = kb
	sent, _ := bot.Send(msg)
	u.LastBotMsgID = sent.MessageID
}

func fmtAmt(v float64) string {
	s := strconv.FormatFloat(v, 'f', ratesDecimals, 64)
	s = strings.TrimRight(s, "0")
	if strings.HasSuffix(s, ".") {
		s += "0"
	}
	return s
}

// --- Catalog (сокращено для примера, у тебя уже есть 60 штук) ---
func buildCatalog() []GPU {
	return []GPU{
		{1, "GeForce GT 710 1GB", 0.0000010, 5},
		{2, "GeForce GT 730 2GB", 0.0000018, 9},
		{3, "GeForce GTX 750 Ti", 0.0000035, 15},
		{4, "GeForce GTX 950", 0.0000070, 30},
		{5, "GeForce GTX 960", 0.0000120, 50},
		{6, "GeForce GTX 970", 0.0000200, 80},
		{7, "GeForce GTX 980", 0.0000300, 120},
		{8, "GeForce GTX 1050 Ti", 0.0000450, 180},
		{9, "GeForce GTX 1060 3GB", 0.0000700, 280},
		{10, "GeForce GTX 1060 6GB", 0.0000900, 360},
		{11, "GeForce GTX 1070", 0.0001300, 520},
		{12, "GeForce GTX 1070 Ti", 0.0001500, 600},
		{13, "GeForce GTX 1080", 0.0001800, 720},
		{14, "GeForce GTX 1080 Ti", 0.0002500, 1000},
		{15, "GeForce RTX 2060", 0.0003000, 1200},
		{16, "GeForce RTX 2060 Super", 0.0003500, 1400},
		{17, "GeForce RTX 2070", 0.0004000, 1600},
		{18, "GeForce RTX 2070 Super", 0.0004500, 1800},
		{19, "GeForce RTX 2080", 0.0005000, 2000},
		{20, "GeForce RTX 2080 Super", 0.0005500, 2200},
		{21, "GeForce RTX 2080 Ti", 0.0007000, 2800},
		{22, "GeForce RTX 3050", 0.0008000, 3200},
		{23, "GeForce RTX 3060", 0.0010000, 4000},
		{24, "GeForce RTX 3060 Ti", 0.0012000, 4800},
		{25, "GeForce RTX 3070", 0.0015000, 6000},
		{26, "GeForce RTX 3070 Ti", 0.0017000, 6800},
		{27, "GeForce RTX 3080 10GB", 0.0020000, 8000},
		{28, "GeForce RTX 3080 12GB", 0.0022000, 8800},
		{29, "GeForce RTX 3080 Ti", 0.0025000, 10000},
		{30, "GeForce RTX 3090", 0.0030000, 12000},
		{31, "GeForce RTX 3090 Ti", 0.0035000, 14000},
		{32, "GeForce RTX 4060", 0.0040000, 16000},
		{33, "GeForce RTX 4060 Ti", 0.0045000, 18000},
		{34, "GeForce RTX 4070", 0.0050000, 20000},
		{35, "GeForce RTX 4070 Ti", 0.0060000, 24000},
		{36, "GeForce RTX 4080", 0.0075000, 30000},
		{37, "GeForce RTX 4080 Super", 0.0080000, 32000},
		{38, "GeForce RTX 4090", 0.0100000, 40000},
		{39, "GeForce RTX 4090 Ti", 0.0120000, 48000},
		{40, "Radeon RX 460", 0.0000050, 20},
		{41, "Radeon RX 470", 0.0000150, 60},
		{42, "Radeon RX 480", 0.0000250, 100},
		{43, "Radeon RX 550", 0.0000080, 32},
		{44, "Radeon RX 560", 0.0000120, 48},
		{45, "Radeon RX 570", 0.0000300, 120},
		{46, "Radeon RX 580", 0.0000450, 180},
		{47, "Radeon RX 590", 0.0000600, 240},
		{48, "Radeon RX Vega 56", 0.0001000, 400},
		{49, "Radeon RX Vega 64", 0.0001300, 520},
		{50, "Radeon VII", 0.0002000, 800},
		{51, "Radeon RX 5500 XT", 0.0002500, 1000},
		{52, "Radeon RX 5600 XT", 0.0003000, 1200},
		{53, "Radeon RX 5700", 0.0003500, 1400},
		{54, "Radeon RX 5700 XT", 0.0004000, 1600},
		{55, "Radeon RX 6600", 0.0005000, 2000},
		{56, "Radeon RX 6600 XT", 0.0006000, 2400},
		{57, "Radeon RX 6700 XT", 0.0008000, 3200},
		{58, "Radeon RX 6800", 0.0010000, 4000},
		{59, "Radeon RX 6800 XT", 0.0012000, 4800},
		{60, "Radeon RX 6900 XT", 0.0015000, 6000},
	}
}
