package utils

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"raahi-backend/models"
	"strings"
	"sync"
	"time"
)

var (
	geoCache      = make(map[string][2]float64)
	geoCacheMutex sync.RWMutex
)

// CacheGeocode allows external controllers to seed the geocode cache
func CacheGeocode(location string, lat, lon float64) {
	cleanLoc := strings.ToLower(strings.TrimSpace(location))
	if cleanLoc == "" {
		return
	}
	geoCacheMutex.Lock()
	geoCache[cleanLoc] = [2]float64{lat, lon}
	geoCacheMutex.Unlock()
}

// HaversineKm calculates the distance between two points in kilometers
func HaversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371.0 // Earth's radius in km
	dLat := (lat2 - lat1) * (math.Pi / 180.0)
	dLon := (lon2 - lon1) * (math.Pi / 180.0)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*(math.Pi/180.0))*math.Cos(lat2*(math.Pi/180.0))*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return R * c
}

// GetRouteFromOSRM fetches the road biology coordinates from OSRM
func GetRouteFromOSRM(startLat, startLon, endLat, endLon float64) ([][2]float64, float64, error) {
	osrmURL := fmt.Sprintf("http://router.project-osrm.org/route/v1/driving/%.6f,%.6f;%.6f,%.6f?overview=full&geometries=polyline",
		startLon, startLat, endLon, endLat)

	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(osrmURL)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	var result struct {
		Routes []struct {
			Geometry string  `json:"geometry"`
			Distance float64 `json:"distance"` // in meters
		} `json:"routes"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, 0, err
	}

	if len(result.Routes) == 0 {
		return nil, 0, fmt.Errorf("no route found")
	}

	// Decode polyline (manual decode for precision or use a library)
	coords := decodePolyline(result.Routes[0].Geometry)
	return coords, result.Routes[0].Distance, nil
}

func decodePolyline(encoded string) [][2]float64 {
	var coords [][2]float64
	var index, lat, lon int

	for index < len(encoded) {
		var b, shift, result int
		for {
			b = int(encoded[index]) - 63
			index++
			result |= (b & 0x1f) << shift
			shift += 5
			if b < 0x20 {
				break
			}
		}
		if result&1 != 0 {
			lat += ^(result >> 1)
		} else {
			lat += result >> 1
		}

		shift, result = 0, 0
		for {
			b = int(encoded[index]) - 63
			index++
			result |= (b & 0x1f) << shift
			shift += 5
			if b < 0x20 {
				break
			}
		}
		if result&1 != 0 {
			lon += ^(result >> 1)
		} else {
			lon += result >> 1
		}

		coords = append(coords, [2]float64{float64(lat) / 1e5, float64(lon) / 1e5})
	}
	return coords
}

// ReverseGeocode turns lat/lon into a location name using Nominatim (OSM)
func ReverseGeocode(lat, lon float64) (string, error) {
	nominatimURL := fmt.Sprintf("https://nominatim.openstreetmap.org/reverse?format=json&lat=%.6f&lon=%.6f&zoom=14&addressdetails=1", lat, lon)

	req, _ := http.NewRequest("GET", nominatimURL, nil)
	req.Header.Set("User-Agent", "RaahiApp/1.0 (contact@raahi.com)")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		DisplayName string `json:"display_name"`
		Address     struct {
			Village string `json:"village"`
			Town    string `json:"town"`
			City    string `json:"city"`
			Suburb  string `json:"suburb"`
			State   string `json:"state"`
		} `json:"address"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	// Prefer village/town/city name over the long display name
	if result.Address.Village != "" {
		return result.Address.Village, nil
	}
	if result.Address.Town != "" {
		return result.Address.Town, nil
	}
	if result.Address.City != "" {
		return result.Address.City, nil
	}
	if result.Address.Suburb != "" {
		return result.Address.Suburb, nil
	}

	// Fallback to first part of display name
	parts := strings.Split(result.DisplayName, ",")
	if len(parts) > 0 {
		return strings.TrimSpace(parts[0]), nil
	}

	return "Unknown Stop", nil
}

// Geocode turns a location name into lat/lon using Nominatim (OSM)
func Geocode(location string) (float64, float64, error) {
	cleanLoc := strings.ToLower(strings.TrimSpace(location))

	// 1. Check in-memory geocode cache
	geoCacheMutex.RLock()
	if coords, ok := geoCache[cleanLoc]; ok {
		geoCacheMutex.RUnlock()
		return coords[0], coords[1], nil
	}
	geoCacheMutex.RUnlock()

	// Hardcoded fix for extremely common local villages missing in OSM/Nominatim
	knownLocations := map[string][2]float64{
		"reema": {29.833, 79.800}, // Reema, Bageshwar district
		"rima":  {29.833, 79.800},
	}

	if coords, ok := knownLocations[cleanLoc]; ok {
		log.Printf("Using hardcoded coordinates for known location: %s -> %v", location, coords)
		return coords[0], coords[1], nil
	}

	// Improve accuracy by ensuring we search in Uttarakhand and specifically for cities/towns
	query := location
	if !strings.Contains(strings.ToLower(location), "india") {
		query = location + ", Uttarakhand, India"
	}

	// Fetch more results to find the best match (e.g. Town vs District center)
	nominatimURL := fmt.Sprintf("https://nominatim.openstreetmap.org/search?format=json&q=%s&limit=5", strings.ReplaceAll(query, " ", "+"))

	req, _ := http.NewRequest("GET", nominatimURL, nil)
	req.Header.Set("User-Agent", "RaahiApp/1.0 (contact@raahi.com)")

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	var results []struct {
		Lat         string `json:"lat"`
		Lon         string `json:"lon"`
		DisplayName string `json:"display_name"`
		Type        string `json:"type"`
		Class       string `json:"class"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return 0, 0, err
	}

	if len(results) == 0 {
		// Fallback only if we haven't already tried a wider India search
		if !strings.Contains(query, "India") {
			return Geocode(location + ", India")
		}
		return 0, 0, fmt.Errorf("location not found: %s", location)
	}

	// Priority types we want for routing
	priorityTypes := map[string]bool{
		"town":    true,
		"city":    true,
		"village": true,
		"suburb":  true,
		"hamlet":  true,
	}

	bestMatchIdx := -1
	for i, res := range results {
		if priorityTypes[res.Type] {
			bestMatchIdx = i
			break
		}
	}

	// If no town/city found in top results, try a second search explicitly for 'town'
	// (Unless we are already searching for town to avoid infinite recursion)
	if bestMatchIdx == -1 && !strings.Contains(strings.ToLower(location), " town") {
		log.Printf("No town/city found for '%s', retrying with ' town' suffix...", location)
		lat, lon, err := Geocode(location + " town")
		if err == nil {
			return lat, lon, nil
		}
	}

	// If still no priority match, pick the first non-administrative result if it exists, otherwise just the first result
	if bestMatchIdx == -1 {
		bestMatchIdx = 0
		for i, res := range results {
			if res.Type != "administrative" && res.Type != "state" {
				bestMatchIdx = i
				break
			}
		}
	}

	log.Printf("Geocoded '%s' (Query: '%s') -> Selected #%d '%s' [Type: %s, Class: %s] (Lat: %s, Lon: %s)",
		location, query, bestMatchIdx+1, results[bestMatchIdx].DisplayName, results[bestMatchIdx].Type, results[bestMatchIdx].Class, results[bestMatchIdx].Lat, results[bestMatchIdx].Lon)

	var lat, lon float64
	fmt.Sscanf(results[bestMatchIdx].Lat, "%f", &lat)
	fmt.Sscanf(results[bestMatchIdx].Lon, "%f", &lon)

	// Update cache
	geoCacheMutex.Lock()
	geoCache[cleanLoc] = [2]float64{lat, lon}
	geoCacheMutex.Unlock()

	return lat, lon, nil
}

// DiscoverIntermediateStops find villages and towns along a road path holistically
func DiscoverIntermediateStops(startLat, startLon, endLat, endLon float64, sampleIntervalKm float64) ([]models.StopInfo, [][2]float64, float64, error) {
	// 1. Get route geometry from OSRM
	routeCoords, totalDistM, err := GetRouteFromOSRM(startLat, startLon, endLat, endLon)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("OSRM route error: %w", err)
	}

	if len(routeCoords) < 2 {
		return nil, routeCoords, totalDistM, fmt.Errorf("route too short")
	}

	// 2. Holistic Discovery using Overpass API (Finds all villages within 1.5km of path)
	rawVillages, err := GetVillagesAlongRoute(routeCoords)
	if err != nil {
		log.Printf("Holistic Overpass discovery failed: %v. Falling back to start/end only.", err)
	}

	// 3. Convert to StopInfo and sort by true distance along the polyline
	var stops []models.StopInfo

	// Calculate cumulative distances along the polyline for accurate stop mapping
	polyDistances := make([]float64, len(routeCoords))
	totalPolyM := 0.0
	for i := 1; i < len(routeCoords); i++ {
		totalPolyM += HaversineKm(routeCoords[i-1][0], routeCoords[i-1][1], routeCoords[i][0], routeCoords[i][1]) * 1000
		polyDistances[i] = totalPolyM
	}

	// Always add start point if geocoded successfully (at 0m)
	startName, _ := ReverseGeocode(startLat, startLon)
	stops = append(stops, models.StopInfo{Name: startName, DistanceM: 0, Lat: startLat, Lon: startLon})

	seenNames := make(map[string]bool)
	seenNames[strings.ToLower(strings.TrimSpace(startName))] = true

	for _, v := range rawVillages {
		// Find the closest point on the route polyline to map the village to a road distance
		minDistToPoly := 1500.0 // 1.5km threshold (same as Overpass radius)
		bestDistOnPoly := -1.0

		for i, coord := range routeCoords {
			d := HaversineKm(v.Lat, v.Lon, coord[0], coord[1]) * 1000
			if d < minDistToPoly {
				minDistToPoly = d
				bestDistOnPoly = polyDistances[i]
			}
		}

		// Skip if too far from road or if distance couldn't be determined
		if bestDistOnPoly < 0 {
			continue
		}

		// Don't add if it's too close to start or end (already handled)
		if bestDistOnPoly < 500 || (totalDistM-bestDistOnPoly) < 500 {
			continue
		}

		// Avoid duplicates with start/end names
		normName := strings.ToLower(strings.TrimSpace(v.Name))
		if seenNames[normName] {
			continue
		}
		seenNames[normName] = true

		stops = append(stops, models.StopInfo{
			Name:      v.Name,
			DistanceM: bestDistOnPoly,
			Lat:       v.Lat,
			Lon:       v.Lon,
		})
	}

	// Add end point (at totalDistM from OSRM)
	endName, _ := ReverseGeocode(endLat, endLon)
	normEndName := strings.ToLower(strings.TrimSpace(endName))
	if !seenNames[normEndName] {
		stops = append(stops, models.StopInfo{Name: endName, DistanceM: totalDistM, Lat: endLat, Lon: endLon})
	}

	// 4. Sequence Sort by DistanceM
	for i := 0; i < len(stops); i++ {
		for j := i + 1; j < len(stops); j++ {
			if stops[i].DistanceM > stops[j].DistanceM {
				stops[i], stops[j] = stops[j], stops[i]
			}
		}
	}

	return stops, routeCoords, totalDistM, nil
}

func FuzzyMatch(s1, s2 string) bool {
	s1 = strings.ToLower(strings.TrimSpace(s1))
	s2 = strings.ToLower(strings.TrimSpace(s2))
	if s1 == "" || s2 == "" {
		return false
	}
	// Strict inclusion check instead of loose soundex/levenshtein
	return strings.Contains(s1, s2) || strings.Contains(s2, s1)
}

// MatchStopInRoute checks if a search term matches any stop in the route.
// Uses case-insensitive substring matching.
// Returns the index and StopInfo if found, or -1 and empty StopInfo if not.
func MatchStopInRoute(stops []models.StopInfo, searchTerm string) (int, models.StopInfo) {
	searchLower := strings.TrimSpace(strings.ToLower(searchTerm))
	if searchLower == "" {
		return -1, models.StopInfo{}
	}

	// First try exact match
	for i, stop := range stops {
		if strings.TrimSpace(strings.ToLower(stop.Name)) == searchLower {
			return i, stop
		}
	}

	// Then try if the search term contains the stop name or vice versa
	for i, stop := range stops {
		if FuzzyMatch(stop.Name, searchLower) {
			return i, stop
		}
	}

	return -1, models.StopInfo{}
}

// CalculateSegmentPrice computes a proportional price for a segment of a route.
// It takes the total route price, total distance, and the pickup/dropoff distances.
func CalculateSegmentPrice(totalPrice, totalDistM, pickupDistM, dropoffDistM float64) float64 {
	if totalDistM <= 0 {
		return totalPrice
	}
	segmentDist := math.Abs(dropoffDistM - pickupDistM)
	ratio := segmentDist / totalDistM
	price := totalPrice * ratio
	// Round to nearest 5 or 10 for convenience
	return math.Round(price/5) * 5
}
