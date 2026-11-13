//go:build windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Config holds the server settings. They are stored as KEY=VALUE lines in
// config.env next to the exe (same keys as .env.example in this repository),
// so they survive restarts and can also be edited with Notepad.
type Config struct {
	ServerName string
	Mode       string
	Port       int
	Password   string
	Maps       string
	MaxPlayers int
	Bots       bool
	MaxBots    int
	PublicIP   string
}

func defaultConfig() Config {
	return Config{
		ServerName: "BAS Community Server",
		Mode:       "dm",
		Port:       40000,
		Maps:       "",
		MaxPlayers: 12,
		MaxBots:    11,
	}
}

func loadConfig(path string) (Config, error) {
	c := defaultConfig()
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\uFEFF"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			v = v[1 : len(v)-1]
		}
		switch k {
		case "BAS_SERVER_NAME":
			c.ServerName = v
		case "BAS_MODE":
			c.Mode = strings.ToLower(v)
		case "BAS_PORT":
			if n, err := strconv.Atoi(v); err == nil {
				c.Port = n
			}
		case "BAS_PASSWORD":
			c.Password = v
		case "BAS_MAPS":
			c.Maps = v
		case "BAS_MAX_PLAYERS":
			if n, err := strconv.Atoi(v); err == nil {
				c.MaxPlayers = n
			}
		case "BAS_BOTS":
			switch strings.ToLower(v) {
			case "true", "1", "yes", "ja", "y":
				c.Bots = true
			default:
				c.Bots = false
			}
		case "BAS_MAX_BOTS":
			if n, err := strconv.Atoi(v); err == nil {
				c.MaxBots = n
			}
		case "BAS_PUBLIC_IP":
			c.PublicIP = v
		}
	}
	return c, sc.Err()
}

func saveConfig(path string, c Config) error {
	var b strings.Builder
	b.WriteString("# BAS: Instagib server settings (written by BAS-Instagib-Server.exe)\r\n")
	b.WriteString("# You can edit this file with Notepad or start the exe with --setup.\r\n")
	fmt.Fprintf(&b, "BAS_SERVER_NAME=%s\r\n", c.ServerName)
	fmt.Fprintf(&b, "BAS_MODE=%s\r\n", c.Mode)
	fmt.Fprintf(&b, "BAS_PORT=%d\r\n", c.Port)
	fmt.Fprintf(&b, "BAS_PASSWORD=%s\r\n", c.Password)
	fmt.Fprintf(&b, "BAS_MAPS=%s\r\n", c.Maps)
	fmt.Fprintf(&b, "BAS_MAX_PLAYERS=%d\r\n", c.MaxPlayers)
	fmt.Fprintf(&b, "BAS_BOTS=%t\r\n", c.Bots)
	fmt.Fprintf(&b, "BAS_MAX_BOTS=%d\r\n", c.MaxBots)
	fmt.Fprintf(&b, "BAS_PUBLIC_IP=%s\r\n", c.PublicIP)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

var mapNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var ipv4Re = regexp.MustCompile(`^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$`)

func (c Config) validate() error {
	if strings.TrimSpace(c.ServerName) == "" {
		return fmt.Errorf("BAS_SERVER_NAME must not be empty")
	}
	if utf8.RuneCountInString(c.ServerName) > 48 {
		return fmt.Errorf("BAS_SERVER_NAME is longer than 48 characters")
	}
	if c.Mode != "dm" && c.Mode != "tdm" {
		return fmt.Errorf("BAS_MODE must be dm or tdm")
	}
	if c.Port < 1024 || c.Port > 65535 {
		return fmt.Errorf("BAS_PORT must be between 1024 and 65535")
	}
	if c.MaxPlayers < 1 || c.MaxPlayers > 12 {
		return fmt.Errorf("BAS_MAX_PLAYERS must be between 1 and 12")
	}
	if c.MaxBots < 0 || c.MaxBots > 11 {
		return fmt.Errorf("BAS_MAX_BOTS must be between 0 and 11")
	}
	if strings.TrimSpace(c.Maps) != "" {
		for _, m := range strings.Split(c.Maps, ",") {
			if !mapNameRe.MatchString(strings.TrimSpace(m)) {
				return fmt.Errorf("invalid map name %q in BAS_MAPS (letters, digits, - and _ only)", m)
			}
		}
	}
	if c.PublicIP != "" {
		m := ipv4Re.FindStringSubmatch(c.PublicIP)
		if m == nil {
			return fmt.Errorf("BAS_PUBLIC_IP must be an IPv4 address or empty")
		}
		for _, p := range m[1:] {
			if n, _ := strconv.Atoi(p); n > 255 {
				return fmt.Errorf("BAS_PUBLIC_IP must be an IPv4 address or empty")
			}
		}
	}
	return nil
}

func (c Config) print() {
	pw := "(none – public server)"
	if c.Password != "" {
		pw = "********"
	}
	ip := "(automatic)"
	if c.PublicIP != "" {
		ip = c.PublicIP
	}
	fmt.Printf("  Server name : %s\n", c.ServerName)
	fmt.Printf("  Mode        : %s\n", c.Mode)
	fmt.Printf("  UDP port    : %d\n", c.Port)
	fmt.Printf("  Password    : %s\n", pw)
	maps := c.Maps
	if strings.TrimSpace(maps) == "" {
		maps = "(automatic – all official maps)"
	}
	fmt.Printf("  Maps        : %s\n", maps)
	fmt.Printf("  Max players : %d\n", c.MaxPlayers)
	if c.Bots {
		fmt.Printf("  Bots        : on (at most %d)\n", c.MaxBots)
	} else {
		fmt.Printf("  Bots        : off\n")
	}
	fmt.Printf("  Public IP   : %s\n", ip)
}

// runSetup asks for every setting, keeping the current value on empty input.
func runSetup(c Config) Config {
	fmt.Println()
	fmt.Println("=== Server settings ===  (press Enter to keep the value in [brackets])")
	for {
		c.ServerName = askString("Server name (max. 48 characters)", c.ServerName)
		c.Mode = strings.ToLower(askString("Game mode: dm (deathmatch) or tdm (team deathmatch)", c.Mode))
		c.Port = askInt("UDP port", c.Port)
		pwDefault := c.Password
		c.Password = askString("Password (empty = public server, '-' = remove password)", pwDefault)
		if c.Password == "-" {
			c.Password = ""
		}
		c.Maps = askString("Map pool, comma-separated (empty or '-' = all official maps)", c.Maps)
		if strings.TrimSpace(c.Maps) == "-" {
			c.Maps = ""
		}
		c.MaxPlayers = askInt("Max players (1-12)", c.MaxPlayers)
		c.Bots = askBool("Fill empty slots with bots (y/n)", c.Bots)
		if c.Bots {
			c.MaxBots = askInt("Max bots (0-11)", c.MaxBots)
		}
		ip := askString("Public IPv4 (empty = automatic, '-' = automatic)", c.PublicIP)
		if ip == "-" {
			ip = ""
		}
		c.PublicIP = ip
		if err := c.validate(); err != nil {
			fmt.Printf("\n!! %v – please try again.\n\n", err)
			continue
		}
		return c
	}
}

func askString(label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, ok := <-stdinLines
	if !ok {
		return def
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func askBool(label string, def bool) bool {
	d := "n"
	if def {
		d = "y"
	}
	for {
		switch strings.ToLower(askString(label, d)) {
		case "y", "yes", "j", "ja", "true", "1":
			return true
		case "n", "no", "nein", "false", "0":
			return false
		}
		fmt.Println("  Please enter y or n.")
	}
}

func askInt(label string, def int) int {
	for {
		s := askString(label, strconv.Itoa(def))
		n, err := strconv.Atoi(s)
		if err == nil {
			return n
		}
		fmt.Println("  Please enter a number.")
	}
}
