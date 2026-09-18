// One-shot credential unlock for nntpprobe. Not part of the product build.
// Claude 2026-09-17: spike helper only — writes env.sh, never prints the password.
package main

import (
	"database/sql"
	"fmt"
	"os"

	"github.com/labbersanon/sakms/internal/secrets"
	_ "modernc.org/sqlite"
)

func main() {
	keyPath := "/tmp/sakms-probe-local/sakms-secret.key"
	dbPath := "/tmp/sakms-probe-local/sakms-probe.db"
	outPath := "/tmp/sakms-probe-local/env.sh"
	if len(os.Args) >= 4 {
		keyPath, dbPath, outPath = os.Args[1], os.Args[2], os.Args[3]
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		panic(err)
	}
	store, err := secrets.New(key)
	if err != nil {
		panic(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		panic(err)
	}
	defer db.Close()
	var user, enc string
	if err := db.QueryRow(`SELECT username, secret_encrypted FROM service_connections WHERE host='news.eweka.nl'`).Scan(&user, &enc); err != nil {
		panic(err)
	}
	pass, err := store.Decrypt(enc)
	if err != nil {
		panic(err)
	}
	content := fmt.Sprintf("export NNTP_USER=%q\nexport NNTP_PASS=%q\n", user, pass)
	if err := os.WriteFile(outPath, []byte(content), 0o600); err != nil {
		panic(err)
	}
	fmt.Printf("wrote %s user=%s pass_len=%d\n", outPath, user, len(pass))
}
