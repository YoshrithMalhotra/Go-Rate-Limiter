package config

import (
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	RedisAddr  string
	ServerPort string
	PolicyFile string
	FailOpen   bool
}

func Load() Config {
	if err := godotenv.Load(); err != nil {
		log.Println("config: no .env file found, reading from environment")
	}

	cfg := Config{
		RedisAddr:  getEnv("REDIS_ADDR", "localhost:6379"),
		ServerPort: ":" + getEnv("PORT", "8080"),
		PolicyFile: getEnv("POLICY_FILE", ""),
		FailOpen:   getBoolEnv("FAIL_OPEN", true),
	}

	if err := cfg.validate(); err != nil {
		log.Fatalf("config: invalid configuration: %v", err)
	}

	log.Printf("config: loaded — Redis: %s  Port: %s", cfg.RedisAddr, cfg.ServerPort)
	return cfg
}

func getEnv(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		return val
	}
	return fallback
}

func getBoolEnv(key string, fallback bool) bool {
	val, ok := os.LookupEnv(key)
	if !ok || val == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(val)
	if err != nil {
		log.Fatalf("config: %s must be true or false", key)
	}
	return parsed
}

func (c *Config) validate() error {
	if c.RedisAddr == "" {
		return fmt.Errorf("REDIS_ADDR is required")
	}
	if c.ServerPort == "" {
		return fmt.Errorf("PORT is required")
	}
	return nil
}
