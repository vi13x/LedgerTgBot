package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	_ "strings"
	"time"

	"github.com/vi13x/bank-lite-cli/internal/cli"
	"github.com/vi13x/bank-lite-cli/internal/service"
	"github.com/vi13x/bank-lite-cli/internal/storage"
)

func main() {
	dataDir := defaultDataDir()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}
	dbPath := filepath.Join(dataDir, "bank.json")

	store, err := storage.OpenFileDB(dbPath)
	if err != nil {
		log.Fatalf("open storage: %v", err)
	}
	defer store.Close()

	bank := service.NewBank(store)
	ui := cli.NewUI(bank, bufio.NewReader(os.Stdin), os.Stdout)

	printBanner()
	for {
		mode := ui.SelectMode()
		switch mode {
		case cli.ModeExit:
			fmt.Fprintln(os.Stdout, "До встречи!")
			return
		case cli.ModeRegister:
			ui.HandleRegister()
		case cli.ModeLogin:
			user := ui.HandleLogin()
			if user != nil {
				ui.HandleSession(user)
			}
		default:
			fmt.Fprintln(os.Stdout, "Неизвестный режим")
		}
	}
}

func defaultDataDir() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = "."
	}
	return filepath.Join(base, "bank-lite-cli")
}

func printBanner() {
	fmt.Println(
		"==============================",
		" BANK LITE CLI",
		"==============================",
	)
	fmt.Println("Время:", time.Now().Format(time.RFC1123))
}
