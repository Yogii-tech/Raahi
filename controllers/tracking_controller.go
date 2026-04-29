package controllers

import (
	"encoding/json"
	"log"
	"net/http"

	"raahi-backend/config"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for the app
	},
}

type LocationUpdate struct {
	RiderID   string  `json:"rider_id"`
	OrderID   string  `json:"order_id"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// PostRiderLocation handles the 5-second polling from the rider app
func PostRiderLocation(c *gin.Context) {
	var update LocationUpdate
	if err := c.ShouldBindJSON(&update); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid location payload"})
		return
	}

	// Publish to Redis channel for this order
	data, _ := json.Marshal(update)
	if config.RDB != nil {
		err := config.RDB.Publish(config.RedisCtx, "tracking:"+update.OrderID, data).Err()
		if err != nil {
			log.Println("Redis publish error:", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to broadcast location"})
			return
		}
	} else {
		log.Println("⚠️ Location update received but Redis is not connected. No broadcast performed.")
	}

	c.JSON(http.StatusAccepted, gin.H{"status": "received"})
}

// TrackOrderWS establishes a WebSocket connection for the customer to receive updates
func TrackOrderWS(c *gin.Context) {
	orderID := c.Param("orderId")
	if orderID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Order ID is required"})
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Println("WebSocket upgrade failed:", err)
		return
	}
	defer conn.Close()

	log.Printf("Customer connected to track order: %s", orderID)

	// Subscribe to Redis channel for this specific order
	if config.RDB == nil {
		conn.WriteJSON(gin.H{"error": "Tracking service currently offline"})
		return
	}

	pubsub := config.RDB.Subscribe(config.RedisCtx, "tracking:"+orderID)
	defer pubsub.Close()

	ch := pubsub.Channel()

	// Keep alive / Wait for messages
	for msg := range ch {
		// Relay the incoming location from Redis directly to the WebSocket client
		err := conn.WriteMessage(websocket.TextMessage, []byte(msg.Payload))
		if err != nil {
			log.Printf("WebSocket write error for order %s: %v", orderID, err)
			break
		}
	}

	log.Printf("Customer disconnected from order: %s", orderID)
}
