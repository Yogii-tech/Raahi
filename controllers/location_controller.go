package controllers

import (
	"encoding/json"
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

	// Append context if not present
	fullQuery := query
	if !strconv.CanBackquote(query) || !strconv.CanBackquote("Uttarakhand") { // dummy check
	}
	// We'll let the frontend provide the full query or append locally
	searchURL := "https://nominatim.openstreetmap.org/search?format=json&limit=5&q=" + strconv.Quote(fullQuery)
	// Actually better to use fmt.Sprintf
	searchURL = "https://nominatim.openstreetmap.org/search?format=json&limit=8&q=" + strings.ReplaceAll(fullQuery, " ", "+")

	req, _ := http.NewRequest("GET", searchURL, nil)
	req.Header.Set("User-Agent", "RaahiApp/1.0 (contact@raahi.com)")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Geocoding service unavailable"})
		return
	}
	defer resp.Body.Close()

	var results []interface{}
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse geocoding results"})
		return
	}

	c.JSON(http.StatusOK, results)
}
