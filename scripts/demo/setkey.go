//go:build ignore

// Setkey stores a Redmine API key in the OS keyring the way yatta's settings
// screen would. Usage: go run scripts/demo/setkey.go <keyring key> <api key>
package main

import (
	"encoding/json"
	"log"
	"os"

	"github.com/gsprdev/yatta/internal/secret"
	"github.com/gsprdev/yatta/internal/ui"
)

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: setkey <keyring key> <api key>")
	}
	cred, err := json.Marshal(ui.Credential{Token: os.Args[2]})
	if err != nil {
		log.Fatal(err)
	}
	if err := (secret.Keyring{}).Set(os.Args[1], string(cred)); err != nil {
		log.Fatal(err)
	}
}
