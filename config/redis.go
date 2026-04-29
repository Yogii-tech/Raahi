package config

import (
	"context"
	"log"

	"github.com/go-redis/redis/v8"
)

var RDB *redis.Client
var RedisCtx = context.Background()

func ConnectRedis() {
	RDB = redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	err := RDB.Ping(RedisCtx).Err()
	if err != nil {
		log.Println("⚠️ Redis not found at localhost:6379. Live tracking will be limited to in-memory relay if needed.")
		// We could fallback to something else, but for now we just log it.
		// In a production app, we'd handle this more robustly.
	} else {
		log.Println("✅ Redis connected successfully for Tracking system")
	}
}
