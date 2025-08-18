// Telegram Miner Simulator Bot — Single file, no external libs
// Changes:
// - Currency switched to USDT
// - New users start with 100 USDT
// - Clean UI with inline buttons (no need to type commands)
// - Self-cleaning: bot keeps only one live UI message per chat (deletes/edits the previous)
// - Bug fixes & hardening (locks, callback handling, message editing/deleting)
//
// Run:
//   export TELEGRAM_BOT_TOKEN=123456:ABC-DEF...
//   go run main.go

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	apiBase       = "https://api.telegram.org/bot"
	dataDir       = "data"
	usersFile     = "data/users.json"
	ratesDecimals = 8 // for formatting small amounts (rates/earnings)

	shopPageSize = 10
	miningWindow = 3 * time.Hour // passive accrual window length

	startBalance = 100.0
	currency     = "USDT"
)

// --- Telegram API payloads ---

type Update struct {
	UpdateID int          `json:"update_id"`
	Message  *Message     `json:"message"`
	Callback *CallbackQry `json:"callback_query"`
}

type Message struct {
	MessageID int     `json:"message_id"`
	From      *UserTG `json:"from"`
	Chat      *Chat   `json:"chat"`
	Date      int64   `json:"date"`
	Text      string  `json:"text"`
}

type UserTG struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
	Language  string `json:"language_code"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type UpdatesResp struct {
	OK     bool     `json:"ok"`
	Result []Update `json:"result"`
}

type SendMessageResp struct {
	OK     bool     `json:"ok"`
	Result *Message `json:"result"`
}

type CallbackQry struct {
	ID      string   `json:"id"`
	From    *UserTG  `json:"from"`
	Data    string   `json:"data"`
	Message *Message `json:"message"`
}

// --- Inline keyboard structures ---

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
}

// --- Game data ---

type GPU struct {
	ID    int     `json:"id"`
	Name  string  `json:"name"`
	Rate  float64 `json:"rate"`  // USDT per second
	Price float64 `json:"price"` // USDT
}

type User struct {
	ID              int64     `json:"id"`
	Username        string    `json:"username"`
	Balance         float64   `json:"balance"`
	Inventory       []int     `json:"inventory"` // GPU IDs
	CreatedAt       time.Time `json:"created_at"`
	LastAccrualAt   time.Time `json:"last_accrual_at"`
	MiningWindowEnd time.Time `json:"mining_window_end"`
	LastBotMsgID    int       `json:"last_bot_msg_id"` // for self-cleaning UI
}

type Store struct {
	Users map[int64]*User `json:"users"`
}

var (
	botToken string
	client   = &http.Client{Timeout: 30 * time.Second}

	// data
	store       Store
	storeMu     sync.RWMutex
	catalog     []GPU
	catalogByID map[int]GPU
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	botToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN is required")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatal(err)
	}

	// Initialize data
	loadOrInitStore()
	catalog = buildCatalog()
	catalogByID = make(map[int]GPU, len(catalog))
	for _, g := range catalog {
		catalogByID[g.ID] = g
	}

	log.Println("Bot is running…")
	runLongPolling()
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
	f, err := os.Open(usersFile)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := json.NewDecoder(f).Decode(&store); err != nil {
		log.Fatal(err)
	}
}

func saveStore() {
	storeMu.Lock()
	defer storeMu.Unlock()
	mustWriteJSON(usersFile, store)
}

func mustWriteJSON(path string, v any) {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		log.Fatal(err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		log.Fatal(err)
	}
	_ = f.Close()
	if err := os.Rename(tmp, path); err != nil {
		log.Fatal(err)
	}
}

// --- Telegram transport ---

func apiURL(method string) string { return apiBase + botToken + "/" + method }

func getUpdates(offset int, timeoutSec int) ([]Update, error) {
	form := url.Values{}
	if offset > 0 {
		form.Set("offset", strconv.Itoa(offset))
	}
	form.Set("timeout", strconv.Itoa(timeoutSec))
	resp, err := client.PostForm(apiURL("getUpdates"), form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var ur UpdatesResp
	if err := json.NewDecoder(resp.Body).Decode(&ur); err != nil {
		return nil, err
	}
	if !ur.OK {
		return nil, fmt.Errorf("getUpdates not ok")
	}
	return ur.Result, nil
}

func sendMessage(chatID int64, text string, kb *InlineKeyboardMarkup) (*Message, error) {
	data := url.Values{}
	data.Set("chat_id", strconv.FormatInt(chatID, 10))
	data.Set("text", text)
	data.Set("parse_mode", "Markdown")
	if kb != nil {
		b, _ := json.Marshal(kb)
		data.Set("reply_markup", string(b))
	}
	resp, err := client.PostForm(apiURL("sendMessage"), data)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var sr SendMessageResp
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, err
	}
	if !sr.OK || sr.Result == nil {
		return nil, fmt.Errorf("sendMessage failed")
	}
	return sr.Result, nil
}

func editMessage(chatID int64, messageID int, text string, kb *InlineKeyboardMarkup) error {
	data := url.Values{}
	data.Set("chat_id", strconv.FormatInt(chatID, 10))
	data.Set("message_id", strconv.Itoa(messageID))
	data.Set("text", text)
	data.Set("parse_mode", "Markdown")
	if kb != nil {
		b, _ := json.Marshal(kb)
		data.Set("reply_markup", string(b))
	}
	_, err := client.PostForm(apiURL("editMessageText"), data)
	return err
}

func deleteMessage(chatID int64, messageID int) error {
	data := url.Values{}
	data.Set("chat_id", strconv.FormatInt(chatID, 10))
	data.Set("message_id", strconv.Itoa(messageID))
	_, err := client.PostForm(apiURL("deleteMessage"), data)
	return err
}

func answerCallback(id string) {
	data := url.Values{}
	data.Set("callback_query_id", id)
	_, _ = client.PostForm(apiURL("answerCallbackQuery"), data)
}

// sendOrReplace ensures only one bot message is kept (self-cleaning UI)
func sendOrReplace(u *User, chatID int64, text string, kb *InlineKeyboardMarkup) {
	// Try to edit existing message; if fails, delete & send a new one
	if u.LastBotMsgID != 0 {
		if err := editMessage(chatID, u.LastBotMsgID, text, kb); err == nil {
			saveStore()
			return
		}
		_ = deleteMessage(chatID, u.LastBotMsgID)
	}
	msg, err := sendMessage(chatID, text, kb)
	if err == nil && msg != nil {
		u.LastBotMsgID = msg.MessageID
		saveStore()
	}
}

// --- Long polling loop ---

func runLongPolling() {
	offset := 0
	for {
		updates, err := getUpdates(offset, 50)
		if err != nil {
			log.Println("getUpdates error:", err)
			time.Sleep(2 * time.Second)
			continue
		}
		for _, up := range updates {
			offset = up.UpdateID + 1
			if up.Message != nil {
				handleMessage(up.Message)
			} else if up.Callback != nil {
				handleCallback(up.Callback)
			}
		}
	}
}

// --- Command & UI handling ---

func handleMessage(m *Message) {
	if m.Chat == nil || m.From == nil {
		return
	}
	chatID := m.Chat.ID
	userID := m.From.ID
	text := strings.TrimSpace(m.Text)

	u := ensureUser(userID, m.From.Username)
	accrueOnInteraction(u)
	saveStore()

	// Support /start and any text -> show menu UI
	switch {
	case strings.HasPrefix(text, "/start"):
		showMainMenu(u, chatID)
	case strings.HasPrefix(text, "/help"):
		showHelp(u, chatID)
	case strings.HasPrefix(text, "/balance"):
		showBalance(u, chatID)
	case strings.HasPrefix(text, "/mine"):
		showMine(u, chatID)
	case strings.HasPrefix(text, "/inventory"):
		showInventory(u, chatID)
	case strings.HasPrefix(text, "/shop"):
		page := 1
		parts := strings.Fields(text)
		if len(parts) > 1 {
			if p, err := strconv.Atoi(parts[1]); err == nil && p > 0 {
				page = p
			}
		}
		showShop(u, chatID, page)
	case strings.HasPrefix(text, "/buy"):
		parts := strings.Fields(text)
		if len(parts) < 2 {
			showInfo(u, chatID, "Использование: /buy <ID>")
			return
		}
		id, err := strconv.Atoi(parts[1])
		if err != nil {
			showInfo(u, chatID, "Неверный ID")
			return
		}
		msg := buyGPU(u, id)
		showInfo(u, chatID, msg)
	case strings.HasPrefix(text, "/sell"):
		parts := strings.Fields(text)
		if len(parts) < 2 {
			showInfo(u, chatID, "Использование: /sell <ID>")
			return
		}
		id, err := strconv.Atoi(parts[1])
		if err != nil {
			showInfo(u, chatID, "Неверный ID")
			return
		}
		msg := sellGPU(u, id)
		showInfo(u, chatID, msg)
	case strings.HasPrefix(text, "/reset"):
		resetUser(u)
		showInfo(u, chatID, "Аккаунт сброшен. Ваш баланс 0 USDT, инвентарь пуст.")
	default:
		showMainMenu(u, chatID)
	}
	saveStore()
}

func handleCallback(cb *CallbackQry) {
	if cb.From == nil || cb.Message == nil {
		return
	}
	chatID := cb.Message.Chat.ID
	u := ensureUser(cb.From.ID, cb.From.Username)
	accrueOnInteraction(u)
	saveStore()

	data := cb.Data
	answerCallback(cb.ID)

	switch {
	case data == "menu":
		showMainMenu(u, chatID)
	case data == "help":
		showHelp(u, chatID)
	case data == "balance":
		showBalance(u, chatID)
	case data == "mine":
		showMine(u, chatID)
	case strings.HasPrefix(data, "shop"):
		page := 1
		parts := strings.Split(data, ":")
		if len(parts) == 2 {
			if p, err := strconv.Atoi(parts[1]); err == nil && p > 0 {
				page = p
			}
		}
		showShop(u, chatID, page)
	case strings.HasPrefix(data, "buy:"):
		id, _ := strconv.Atoi(strings.TrimPrefix(data, "buy:"))
		msg := buyGPU(u, id)
		showInfo(u, chatID, msg)
	case data == "inventory":
		showInventory(u, chatID)
	case strings.HasPrefix(data, "sell:"):
		id, _ := strconv.Atoi(strings.TrimPrefix(data, "sell:"))
		msg := sellGPU(u, id)
		showInfo(u, chatID, msg)
	case data == "reset":
		resetUser(u)
		showInfo(u, chatID, "Аккаунт сброшен. Баланс 0 USDT, инвентарь пуст.")
	default:
		showMainMenu(u, chatID)
	}
	saveStore()
}

// --- Screens (UI) ---

func showMainMenu(u *User, chatID int64) {
	text := fmt.Sprintf(
		"👋 Добро пожаловать в *GPU Miner Simulator*\n\n"+
			"Баланс: *%s %s*\nСкорость добычи: *%s %s/сек*\n"+
			"Пассивная добыча действует %s после последней активности.",
		fmtAmt(u.Balance), currency, fmtAmt(totalRate(u)), currency, durShort(miningWindow),
	)
	kb := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{{Text: "💰 Баланс", CallbackData: "balance"}, {Text: "⛏️ Добыча", CallbackData: "mine"}},
		{{Text: "🛒 Магазин", CallbackData: "shop:1"}, {Text: "🎒 Инвентарь", CallbackData: "inventory"}},
		{{Text: "ℹ️ Помощь", CallbackData: "help"}, {Text: "♻️ Сброс", CallbackData: "reset"}},
	}}
	sendOrReplace(u, chatID, text, kb)
}

func showHelp(u *User, chatID int64) {
	var b strings.Builder
	fmt.Fprintf(&b, "Команды (для совместимости — но удобнее кнопками):\n")
	fmt.Fprintf(&b, "/start — меню\n")
	fmt.Fprintf(&b, "/balance — баланс и скорость\n")
	fmt.Fprintf(&b, "/mine — статус добычи\n")
	fmt.Fprintf(&b, "/inventory — ваши видеокарты\n")
	fmt.Fprintf(&b, "/shop [страница] — магазин (по %d на страницу)\n", shopPageSize)
	fmt.Fprintf(&b, "/buy <ID> — купить карту\n")
	fmt.Fprintf(&b, "/sell <ID> — продать карту за 80%% цены\n")
	fmt.Fprintf(&b, "/reset — сброс аккаунта")
	kb := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{{Text: "⬅️ Назад в меню", CallbackData: "menu"}},
	}}
	sendOrReplace(u, chatID, b.String(), kb)
}

func showBalance(u *User, chatID int64) {
	text := fmt.Sprintf("Ваш баланс: *%s %s*\nТекущая скорость: *%s %s/сек*",
		fmtAmt(u.Balance), currency, fmtAmt(totalRate(u)), currency)
	kb := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{{Text: "⬅️ Назад", CallbackData: "menu"}},
	}}
	sendOrReplace(u, chatID, text, kb)
}

func showMine(u *User, chatID int64) {
	left := time.Until(u.MiningWindowEnd)
	if left < 0 {
		left = 0
	}
	text := fmt.Sprintf("⛏️ Пассивная добыча активна.\nОкно истечёт через: *%s*\nСкорость: *%s %s/сек*",
		durShort(left), fmtAmt(totalRate(u)), currency)
	kb := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{{Text: "⬅️ Назад", CallbackData: "menu"}},
	}}
	sendOrReplace(u, chatID, text, kb)
}

func showShop(u *User, chatID int64, page int) {
	if page < 1 {
		page = 1
	}
	total := len(catalog)
	pages := int(math.Ceil(float64(total) / float64(shopPageSize)))
	if pages == 0 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	start := (page - 1) * shopPageSize
	end := start + shopPageSize
	if end > total {
		end = total
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🛒 *Магазин видеокарт* (стр. %d/%d)\nБаланс: %s %s\n\n", page, pages, fmtAmt(u.Balance), currency)
	rows := [][]InlineKeyboardButton{}
	for i := start; i < end; i++ {
		g := catalog[i]
		fmt.Fprintf(&b, "ID %d — %s\nЦена: %s %s | Добыча: %s %s/сек\n", g.ID, g.Name, fmtAmt(g.Price), currency, fmtAmt(g.Rate), currency)
		rows = append(rows, []InlineKeyboardButton{{Text: "Купить: " + g.Name, CallbackData: fmt.Sprintf("buy:%d", g.ID)}})
	}
	nav := []InlineKeyboardButton{}
	if page > 1 {
		nav = append(nav, InlineKeyboardButton{Text: "◀️", CallbackData: fmt.Sprintf("shop:%d", page-1)})
	}
	nav = append(nav, InlineKeyboardButton{Text: "⬅️ Меню", CallbackData: "menu"})
	if page < pages {
		nav = append(nav, InlineKeyboardButton{Text: "▶️", CallbackData: fmt.Sprintf("shop:%d", page+1)})
	}
	rows = append(rows, nav)
	kb := &InlineKeyboardMarkup{InlineKeyboard: rows}
	sendOrReplace(u, chatID, b.String(), kb)
}

func showInventory(u *User, chatID int64) {
	if len(u.Inventory) == 0 {
		kb := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
			{{Text: "🛒 В магазин", CallbackData: "shop:1"}, {Text: "⬅️ Меню", CallbackData: "menu"}},
		}}
		sendOrReplace(u, chatID, "Инвентарь пуст.", kb)
		return
	}
	countBy := map[int]int{}
	for _, id := range u.Inventory {
		countBy[id]++
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🎒 *Ваши видеокарты* (всего %d)\n", len(u.Inventory))
	rows := [][]InlineKeyboardButton{}
	var rate, totalPrice float64
	for _, id := range uniqueInts(u.Inventory) {
		g := catalogByID[id]
		cnt := countBy[id]
		rate += g.Rate * float64(cnt)
		totalPrice += g.Price * float64(cnt)
		fmt.Fprintf(&b, "%dx %s — %s %s/сек\n", cnt, g.Name, fmtAmt(g.Rate*float64(cnt)), currency)
		rows = append(rows, []InlineKeyboardButton{{Text: fmt.Sprintf("Продать 1: %s", g.Name), CallbackData: fmt.Sprintf("sell:%d", g.ID)}})
	}
	fmt.Fprintf(&b, "\nСкорость всего: *%s %s/сек*\nОценка (80%%): ~%s %s\n", fmtAmt(rate), currency, fmtAmt(totalPrice*0.8), currency)
	rows = append(rows, []InlineKeyboardButton{{Text: "🛒 Магазин", CallbackData: "shop:1"}, {Text: "⬅️ Меню", CallbackData: "menu"}})
	kb := &InlineKeyboardMarkup{InlineKeyboard: rows}
	sendOrReplace(u, chatID, b.String(), kb)
}

func showInfo(u *User, chatID int64, msg string) {
	kb := &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{
		{{Text: "⬅️ Меню", CallbackData: "menu"}, {Text: "🛒 Магазин", CallbackData: "shop:1"}, {Text: "🎒 Инвентарь", CallbackData: "inventory"}},
	}}
	sendOrReplace(u, chatID, msg, kb)
}

// --- User lifecycle & accrual ---

func ensureUser(id int64, username string) *User {
	storeMu.Lock()
	defer storeMu.Unlock()
	u, ok := store.Users[id]
	if !ok {
		u = &User{
			ID:              id,
			Username:        username,
			Balance:         startBalance,
			Inventory:       []int{},
			CreatedAt:       time.Now().UTC(),
			LastAccrualAt:   time.Now().UTC(),
			MiningWindowEnd: time.Now().UTC().Add(miningWindow),
			LastBotMsgID:    0,
		}
		store.Users[id] = u
	}
	return u
}

// Accrue passive earnings from LastAccrualAt up to min(now, MiningWindowEnd).
// Then refresh the 3h window starting at now.
func accrueOnInteraction(u *User) {
	now := time.Now().UTC()
	accrualEnd := u.MiningWindowEnd
	if now.Before(accrualEnd) {
		accrualEnd = now
	}
	if accrualEnd.After(u.LastAccrualAt) {
		seconds := accrualEnd.Sub(u.LastAccrualAt).Seconds()
		inc := totalRate(u) * seconds
		u.Balance += inc
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

// --- Shop / Inventory logic ---

func buyGPU(u *User, id int) string {
	g, ok := catalogByID[id]
	if !ok {
		return "Такого товара нет"
	}
	if u.Balance < g.Price {
		return fmt.Sprintf("Недостаточно средств. Нужно %s %s", fmtAmt(g.Price), currency)
	}
	u.Balance -= g.Price
	u.Inventory = append(u.Inventory, g.ID)
	return fmt.Sprintf("Куплено: *%s*. Остаток: %s %s. Скорость: %s %s/сек", g.Name, fmtAmt(u.Balance), currency, fmtAmt(totalRate(u)), currency)
}

func sellGPU(u *User, id int) string {
	idx := -1
	for i, v := range u.Inventory {
		if v == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return "У вас нет карты с таким ID"
	}
	g := catalogByID[id]
	// remove one instance
	u.Inventory = append(u.Inventory[:idx], u.Inventory[idx+1:]...)
	refund := g.Price * 0.8
	u.Balance += refund
	return fmt.Sprintf("Продано: *%s* за %s %s. Баланс: %s %s. Скорость: %s %s/сек", g.Name, fmtAmt(refund), currency, fmtAmt(u.Balance), currency, fmtAmt(totalRate(u)), currency)
}

func resetUser(u *User) {
	u.Balance = 0
	u.Inventory = nil
	u.LastAccrualAt = time.Now().UTC()
	u.MiningWindowEnd = time.Now().UTC().Add(miningWindow)
}

// --- Catalog (60 GPUs) ---

func buildCatalog() []GPU {
	var list []GPU
	seed := []struct {
		name  string
		rate  float64
		price float64
	}{
		{"GeForce GT 710 1GB", 0.0000010, 5},
		{"GeForce GT 730 2GB", 0.0000018, 9},
		{"Radeon R7 240", 0.0000025, 12},
		{"GeForce GTX 750 Ti", 0.0000040, 20},
		{"Radeon RX 460", 0.0000060, 30},
		{"GeForce GTX 950", 0.0000085, 42},
		{"Radeon RX 560", 0.0000120, 60},
		{"GeForce GTX 1050", 0.0000180, 90},
		{"GeForce GTX 1050 Ti", 0.0000250, 125},
		{"Radeon RX 570 4GB", 0.0000400, 200},
		{"GeForce GTX 1060 3GB", 0.0000500, 250},
		{"GeForce GTX 1060 6GB", 0.0000600, 300},
		{"Radeon RX 580 8GB", 0.0000750, 375},
		{"GeForce GTX 1070", 0.0001000, 500},
		{"GeForce GTX 1070 Ti", 0.0001200, 600},
		{"Radeon VII", 0.0001500, 750},
		{"GeForce GTX 1080", 0.0001800, 900},
		{"GeForce GTX 1080 Ti", 0.0002200, 1100},
		{"Radeon RX 5600 XT", 0.0002600, 1300},
		{"Radeon RX 5700", 0.0003000, 1500},
		{"Radeon RX 5700 XT", 0.0003500, 1750},
		{"GeForce RTX 2060", 0.0004000, 2000},
		{"GeForce RTX 2060 Super", 0.0004700, 2350},
		{"GeForce RTX 2070", 0.0005400, 2700},
		{"GeForce RTX 2070 Super", 0.0006200, 3100},
		{"GeForce RTX 2080", 0.0007000, 3500},
		{"GeForce RTX 2080 Super", 0.0007800, 3900},
		{"GeForce RTX 2080 Ti", 0.0009000, 4500},
		{"Radeon RX 6600", 0.0010000, 5000},
		{"Radeon RX 6600 XT", 0.0011500, 5750},
		{"Radeon RX 6700 XT", 0.0013500, 6750},
		{"GeForce RTX 3060 12GB", 0.0015000, 7500},
		{"GeForce RTX 3060 Ti", 0.0018000, 9000},
		{"GeForce RTX 3070", 0.0021000, 10500},
		{"GeForce RTX 3070 Ti", 0.0024000, 12000},
		{"GeForce RTX 3080 10GB", 0.0028000, 14000},
		{"GeForce RTX 3080 Ti", 0.0032000, 16000},
		{"Radeon RX 6800", 0.0034000, 17000},
		{"Radeon RX 6800 XT", 0.0038000, 19000},
		{"Radeon RX 6900 XT", 0.0042000, 21000},
		{"GeForce RTX 3090", 0.0048000, 24000},
		{"GeForce RTX 3090 Ti", 0.0055000, 27500},
		{"Radeon RX 7600", 0.0060000, 30000},
		{"Radeon RX 7700 XT", 0.0070000, 35000},
		{"Radeon RX 7800 XT", 0.0080000, 40000},
		{"GeForce RTX 4060", 0.0085000, 42500},
		{"GeForce RTX 4060 Ti", 0.0095000, 47500},
		{"GeForce RTX 4070", 0.0105000, 52500},
		{"GeForce RTX 4070 Ti", 0.0120000, 60000},
		{"GeForce RTX 4070 Ti Super", 0.0135000, 67500},
		{"Radeon RX 7900 GRE", 0.0140000, 70000},
		{"Radeon RX 7900 XT", 0.0155000, 77500},
		{"Radeon RX 7900 XTX", 0.0170000, 85000},
		{"GeForce RTX 4080 12GB (Super)", 0.0180000, 90000},
		{"GeForce RTX 4080 16GB", 0.0190000, 95000},
		{"GeForce RTX 4080 Super", 0.0200000, 100000},
		{"GeForce RTX 4090", 0.0220000, 110000},
		{"GeForce RTX 4090 D", 0.0230000, 115000},
		{"GeForce RTX 4090 Ti (myth)", 0.0250000, 125000},
	}
	for i, s := range seed {
		list = append(list, GPU{ID: i + 1, Name: s.name, Rate: s.rate, Price: s.price})
	}
	return list
}

// --- Utils ---

func fmtAmt(v float64) string {
	s := strconv.FormatFloat(v, 'f', ratesDecimals, 64)
	s = strings.TrimRight(s, "0")
	if strings.HasSuffix(s, ".") {
		s += "0"
	}
	return s
}

func durShort(d time.Duration) string {
	if d <= 0 {
		return "0с"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	sb := &strings.Builder{}
	if h > 0 {
		fmt.Fprintf(sb, "%dч", h)
	}
	if m > 0 {
		if sb.Len() > 0 {
			sb.WriteString(" ")
		}
		fmt.Fprintf(sb, "%dм", m)
	}
	if h == 0 && m == 0 {
		fmt.Fprintf(sb, "%dс", s)
	}
	return sb.String()
}

func uniqueInts(a []int) []int {
	m := map[int]struct{}{}
	var out []int
	for _, v := range a {
		if _, ok := m[v]; !ok {
			m[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}

// --- END ---
