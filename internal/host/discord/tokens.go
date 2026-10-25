package discord

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/crew/keystore"
)

// KeyringService is the credential-store service bot tokens are filed under (one entry per bot id).
const KeyringService = "arkism-discord"

// fallbackFile keeps tokens (0600) on a host with no credential store, like provider keys on a headless server.
var fallbackFile = host.Env("ARKISM_DISCORD_TOKENS", filepath.Join(host.AgentsState, "discord-tokens.json"))

var tokenMu sync.Mutex

func ref(botID string) string { return "keyring:" + KeyringService + "/" + botID }

// saveToken stores a bot token: the OS keyring when one answers, else the 0600 file. It never logs the token.
func saveToken(botID, token string) (storage string, err error) {
	if _, err := keystore.PutIn(KeyringService, botID, token); err == nil {
		// A stale file copy must not outlive a rotated token.
		if err := fileToken(botID, "", true); err != nil {
			return keystore.StorageKeyring, fmt.Errorf("stored in the keyring, but the old file copy stays: %w", err)
		}
		return keystore.StorageKeyring, nil
	}
	return keystore.StorageFile, fileToken(botID, token, false)
}

// loadToken reads a bot token back ("" when none is stored).
func loadToken(botID string) string {
	if keystore.Available() {
		if v, err := keystore.Get(ref(botID)); err == nil {
			return v
		}
	}
	tokenMu.Lock()
	defer tokenMu.Unlock()
	m := readFile()
	return m[botID]
}

// dropToken removes a bot's token everywhere (bot archived).
func dropToken(botID string) error {
	var err error
	if keystore.Available() {
		err = keystore.Delete(ref(botID))
	}
	return errors.Join(err, fileToken(botID, "", true))
}

func readFile() map[string]string {
	m := map[string]string{}
	raw, err := os.ReadFile(fallbackFile)
	if err != nil {
		return m
	}
	if st, err := os.Stat(fallbackFile); err == nil && st.Mode().Perm()&0o077 != 0 {
		return map[string]string{} // others can read it: refuse, like the provider keys file
	}
	_ = json.Unmarshal(raw, &m)
	return m
}

func fileToken(botID, token string, remove bool) error {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	m := readFile()
	if remove {
		if _, ok := m[botID]; !ok {
			return nil
		}
		delete(m, botID)
	} else {
		m[botID] = token
	}
	if err := os.MkdirAll(filepath.Dir(fallbackFile), 0o700); err != nil {
		return err
	}
	raw, _ := json.Marshal(m)
	return host.AtomicWrite(fallbackFile, string(raw))
}

func last4(token string) string {
	r := []rune(token)
	if len(r) > 4 {
		r = r[len(r)-4:]
	}
	return string(r)
}
