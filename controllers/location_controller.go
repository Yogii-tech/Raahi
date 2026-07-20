package controllers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"raahi-backend/utils"

	"github.com/gin-gonic/gin"
)

func GetNearbyLandmarks(c *gin.Context) {
	latStr := c.Query("lat")
	lonStr := c.Query("lon")

	lat, errLat := strconv.ParseFloat(latStr, 64)
	lon, errLon := strconv.ParseFloat(lonStr, 64)

	if errLat != nil || errLon != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid or missing lat/lon parameters"})
		return
	}

	landmarks, err := utils.GetNearbyLandmarks(lat, lon)
	if err != nil {
		log.Printf("❌ Failed to fetch landmarks from Overpass: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch nearby landmarks"})
		return
	}

	if landmarks == nil {
		landmarks = []utils.Landmark{} // ensure array is never null in JSON
	}

	c.JSON(http.StatusOK, landmarks)
}

func SearchLocations(c *gin.Context) {
	query := c.Query("q")
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Query parameter 'q' is required"})
		return
	}

	searchURL := "https://nominatim.openstreetmap.org/search?format=json&limit=8&q=" + strings.ReplaceAll(query, " ", "+")

	req, _ := http.NewRequest("GET", searchURL, nil)
	req.Header.Set("User-Agent", "RaahiApp/1.0 (contact@raahi.com)")

	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Geocoding service unavailable"})
		return
	}
	defer resp.Body.Close()

	var results []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse geocoding results"})
		return
	}

	// Cache geography coordinate results to prevent subsequent slow Nominatim api duplicate queries
	for _, res := range results {
		latStr, okLat := res["lat"].(string)
		lonStr, okLon := res["lon"].(string)
		displayStr, okDisp := res["display_name"].(string)
		if okLat && okLon && okDisp {
			var lat, lon float64
			if _, errLat := fmt.Sscanf(latStr, "%f", &lat); errLat == nil {
				if _, errLon := fmt.Sscanf(lonStr, "%f", &lon); errLon == nil {
					// Cache full display name
					utils.CacheGeocode(displayStr, lat, lon)

					// Cache first element of display name (e.g. "Haldwani")
					parts := strings.Split(displayStr, ",")
					if len(parts) > 0 {
						utils.CacheGeocode(parts[0], lat, lon)
					}
				}
			}
		}
	}

	c.JSON(http.StatusOK, results)
}
