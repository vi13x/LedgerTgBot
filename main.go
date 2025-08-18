// Telegram Miner Simulator Bot
// Requirements:
// - Pure Go stdlib only (no third‑party libs), uses Telegram Bot API via HTTPS
// - Simulates GPU "mining" with purchasable video cards (50+ models), each with its own
//   price and passive mining rate
// - "Autonomous" mining continues up to 3 hours after the user's last interaction
//   (mining window). When the user returns, accrues earnings up to the window end,
//   then starts a fresh 3-hour window.
// - Simple JSON file persistence
// - Basic commands: /start, /help, /balance, /mine, /inventory, /shop [page], /buy <id>, /sell <id>, /reset
//
// Usage:
//   export TELEGRAM_BOT_TOKEN=123456:ABC-DEF...
//   go run main.go
//
// Notes:
// - Currency unit here is MNT ("miner tokens")
// - Mining rate units are MNT per second
// - Prices are in MNT
// - This is a minimal but production-ready foundation; consider running behind a process
//   supervisor and backing up the data folder.

package main

import (
	"bytes"
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
	ratesDecimals = 8 // for formatting MNT amounts

	shopPageSize = 10
	miningWindow = 3 * time.Hour // passive accrual window length
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

// --- Game data ---

type GPU struct {
	ID    int     `json:"id"`
	Name  string  `json:"name"`
	Rate  float64 `json:"rate"`  // MNT per second
	Price float64 `json:"price"` // MNT
}

type User struct {
	ID              int64     `json:"id"`
	Username        string    `json:"username"`
	Balance         float64   `json:"balance"`
	Inventory       []int     `json:"inventory"` // GPU IDs
	CreatedAt       time.Time `json:"created_at"`
	LastAccrualAt   time.Time `json:"last_accrual_at"`
	MiningWindowEnd time.Time `json:"mining_window_end"`
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
	storeMu.RLock()
	defer storeMu.RUnlock()
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
	f.Close()
	if err := os.Rename(tmp, path); err != nil {
		log.Fatal(err)
	}
}

// --- Telegram transport ---

func apiURL(method string) string {
	return apiBase + botToken + "/" + method
}

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

func sendMessage(chatID int64, text string) error {
	data := url.Values{}
	data.Set("chat_id", strconv.FormatInt(chatID, 10))
	data.Set("text", text)
	data.Set("parse_mode", "Markdown")
	resp, err := client.PostForm(apiURL("sendMessage"), data)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var sr SendMessageResp
	_ = json.NewDecoder(resp.Body).Decode(&sr)
	return nil
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
				// Not used in this minimal version
			}
		}
	}
}

// --- Command handling ---

func handleMessage(m *Message) {
	if m.Chat == nil || m.From == nil {
		return
	}
	chatID := m.Chat.ID
	userID := m.From.ID
	text := strings.TrimSpace(m.Text)
	if text == "" {
		return
	}

	// ensure user exists & accrue on any interaction
	u := ensureUser(userID, m.From.Username)
	accrueOnInteraction(u)
	saveStore()

	switch {
	case strings.HasPrefix(text, "/start"):
		_ = sendMessage(chatID, welcomeText(u))
	case strings.HasPrefix(text, "/help"):
		_ = sendMessage(chatID, helpText())
	case strings.HasPrefix(text, "/balance"):
		_ = sendMessage(chatID, fmt.Sprintf("Ваш баланс: *%s MNT*\nСкорость добычи: *%s MNT/сек*", fmtAmt(u.Balance), fmtAmt(totalRate(u))))
	case strings.HasPrefix(text, "/mine"):
		left := time.Until(u.MiningWindowEnd)
		if left < 0 {
			left = 0
		}
		_ = sendMessage(chatID, fmt.Sprintf("Добыча активна. Окно пассивной добычи истечет через: *%s*\nТекущая скорость: *%s MNT/сек*", durShort(left), fmtAmt(totalRate(u))))
	case strings.HasPrefix(text, "/inventory"):
		_ = sendMessage(chatID, inventoryText(u))
	case strings.HasPrefix(text, "/shop"):
		page := 1
		parts := strings.Fields(text)
		if len(parts) > 1 {
			if p, err := strconv.Atoi(parts[1]); err == nil && p > 0 {
				page = p
			}
		}
		_ = sendMessage(chatID, shopPageText(page))
	case strings.HasPrefix(text, "/buy"):
		parts := strings.Fields(text)
		if len(parts) < 2 {
			_ = sendMessage(chatID, "Использование: /buy <ID>")
			return
		}
		id, err := strconv.Atoi(parts[1])
		if err != nil {
			_ = sendMessage(chatID, "Неверный ID")
			return
		}
		msg := buyGPU(u, id)
		_ = sendMessage(chatID, msg)
	case strings.HasPrefix(text, "/sell"):
		parts := strings.Fields(text)
		if len(parts) < 2 {
			_ = sendMessage(chatID, "Использование: /sell <ID>")
			return
		}
		id, err := strconv.Atoi(parts[1])
		if err != nil {
			_ = sendMessage(chatID, "Неверный ID")
			return
		}
		msg := sellGPU(u, id)
		_ = sendMessage(chatID, msg)
	case strings.HasPrefix(text, "/reset"):
		resetUser(u)
		_ = sendMessage(chatID, "Аккаунт сброшен. Ваш баланс 0 MNT, инвентарь пуст. /shop для покупок.")
	default:
		_ = sendMessage(chatID, "Неизвестная команда. Напишите /help")
	}
	// persist after command
	saveStore()
}

func welcomeText(u *User) string {
	b := &bytes.Buffer{}
	fmt.Fprintf(b, "👋 Добро пожаловать в *GPU Miner Simulator*!\n\n")
	fmt.Fprintf(b, "Ваш баланс: *%s MNT*\nСкорость добычи: *%s MNT/сек*\n\n", fmtAmt(u.Balance), fmtAmt(totalRate(u)))
	fmt.Fprintf(b, "Пассивная добыча продолжается *%s* после последнего визита. Возвращайтесь чаще!\n\n", durShort(miningWindow))
	fmt.Fprintf(b, "Команды:\n%s", helpText())
	return b.String()
}

func helpText() string {
	return "" +
		"/start — создать аккаунт / приветствие\n" +
		"/help — помощь\n" +
		"/balance — баланс и скорость\n" +
		"/mine — статус добычи\n" +
		"/inventory — ваши видеокарты\n" +
		"/shop [страница] — магазин (по %d на страницу)\n" +
		"/buy <ID> — купить карту\n" +
		"/sell <ID> — продать карту за 80%% цены\n" +
		"/reset — сброс аккаунта"
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
			Balance:         0,
			Inventory:       []int{},
			CreatedAt:       time.Now().UTC(),
			LastAccrualAt:   time.Now().UTC(),
			MiningWindowEnd: time.Now().UTC().Add(miningWindow),
		}
		store.Users[id] = u
	}
	return u
}

// accrueOnInteraction applies passive mining earnings.
// It accrues from LastAccrualAt up to min(now, MiningWindowEnd).
// Then it refreshes MiningWindowEnd to now + 3h and sets LastAccrualAt to now.
func accrueOnInteraction(u *User) {
	now := time.Now().UTC()
	// Earnings window
	end := u.MiningWindowEnd
	if now.After(end) {
		end = u.MiningWindowEnd
	} else {
		end = now
	}
	if end.After(u.LastAccrualAt) {
		seconds := end.Sub(u.LastAccrualAt).Seconds()
		rate := totalRate(u)
		inc := rate * seconds
		u.Balance += inc
	}
	// refresh window starting now
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

// --- Shop / Inventory ---

func shopPageText(page int) string {
	if page < 1 {
		page = 1
	}
	total := len(catalog)
	pages := int(math.Ceil(float64(total) / float64(shopPageSize)))
	if page > pages {
		page = pages
	}
	start := (page - 1) * shopPageSize
	end := start + shopPageSize
	if end > total {
		end = total
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🛒 *Магазин видеокарт* (страница %d/%d)\n", page, pages)
	for i := start; i < end; i++ {
		g := catalog[i]
		fmt.Fprintf(&b, "ID %d — %s\nЦена: %s MNT | Добыча: %s MNT/сек\n", g.ID, g.Name, fmtAmt(g.Price), fmtAmt(g.Rate))
	}
	fmt.Fprintf(&b, "\nИспользуйте: /buy <ID>\nСледующая страница: /shop %d", minInt(page+1, pages))
	return b.String()
}

func inventoryText(u *User) string {
	if len(u.Inventory) == 0 {
		return "Инвентарь пуст. Зайдите в /shop"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🎒 *Ваши видеокарты* (всего %d)\n", len(u.Inventory))
	var totalPrice, rate float64
	countBy := map[int]int{}
	for _, id := range u.Inventory {
		countBy[id]++
	}
	for _, id := range uniqueInts(u.Inventory) {
		g := catalogByID[id]
		cnt := countBy[id]
		fmt.Fprintf(&b, "%dx %s — суммарная добыча %s MNT/сек\n", cnt, g.Name, fmtAmt(g.Rate*float64(cnt)))
		rate += g.Rate * float64(cnt)
		totalPrice += g.Price * float64(cnt)
	}
	fmt.Fprintf(&b, "\nИтого скорость: *%s MNT/сек*\nТеор. стоимость: ~%s MNT\n", fmtAmt(rate), fmtAmt(totalPrice*0.8))
	fmt.Fprintf(&b, "Продажа: /sell <ID> (80%% цены за штуку)")
	return b.String()
}

func buyGPU(u *User, id int) string {
	g, ok := catalogByID[id]
	if !ok {
		return "Такого товара нет"
	}
	if u.Balance < g.Price {
		return fmt.Sprintf("Недостаточно средств. Нужно %s MNT", fmtAmt(g.Price))
	}
	u.Balance -= g.Price
	u.Inventory = append(u.Inventory, g.ID)
	return fmt.Sprintf("Куплено: *%s*. Остаток: %s MNT. Текущая скорость: %s MNT/сек", g.Name, fmtAmt(u.Balance), fmtAmt(totalRate(u)))
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
	return fmt.Sprintf("Продано: *%s* за %s MNT. Баланс: %s MNT. Скорость: %s MNT/сек", g.Name, fmtAmt(refund), fmtAmt(u.Balance), fmtAmt(totalRate(u)))
}

func resetUser(u *User) {
	u.Balance = 0
	u.Inventory = nil
	u.LastAccrualAt = time.Now().UTC()
	u.MiningWindowEnd = time.Now().UTC().Add(miningWindow)
}

// --- Catalog (60 GPUs) ---

func buildCatalog() []GPU {
	// Rates grow roughly exponentially; prices scale ~rate * factor
	// Base rate ~1e-6 MNT/s (very weak), top ~2e-2 MNT/s (very strong)
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
	// Trim trailing zeros
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- END ---
