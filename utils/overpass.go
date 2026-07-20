package utils

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Simple in-memory cache to prevent lag
var (
	discoveryCache = make(map[string]cacheEntry)
	cacheMutex     sync.RWMutex
)

type cacheEntry struct {
	data      interface{}
	timestamp time.Time
}

const cacheExpiration = 1 * time.Hour

type Landmark struct {
	Name      string  `json:"name"`
	Type      string  `json:"type"`
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	DistanceM float64 `json:"distanceM"`
}

type OverpassResponse struct {
	Elements []struct {
		Type   string  `json:"type"`
		Id     int64   `json:"id"`
		Lat    float64 `json:"lat"`
		Lon    float64 `json:"lon"`
		Center struct {
			Lat float64 `json:"lat"`
			Lon float64 `json:"lon"`
		} `json:"center"`
		Tags map[string]string `json:"tags"`
	} `json:"elements"`
}

func GetNearbyLandmarks(lat, lon float64) ([]Landmark, error) {
	cacheKey := fmt.Sprintf("landmarks:%.3f,%.3f", lat, lon)

	cacheMutex.RLock()
	if entry, ok := discoveryCache[cacheKey]; ok {
		if time.Since(entry.timestamp) < cacheExpiration {
			cacheMutex.RUnlock()
			return entry.data.([]Landmark), nil
		}
	}
	cacheMutex.RUnlock()

	// Radius 3000m (3km)
	query := fmt.Sprintf(`[out:json][timeout:25];
(
  nwr(around:3000, %f, %f)["amenity"~"^(bus_station|taxi|restaurant|place_of_worship)$"];
  nwr(around:3000, %f, %f)["tourism"~"^(viewpoint|hotel)$"];
  nwr(around:3000, %f, %f)["shop"="convenience"];
  nwr(around:3000, %f, %f)["historic"="monument"];
);
out center;`, lat, lon, lat, lon, lat, lon, lat, lon)

	apiURL := "https://overpass-api.de/api/interpreter"
	data := url.Values{}
	data.Set("data", query)

	req, _ := http.NewRequest("POST", apiURL, strings.NewReader(data.Encode()))
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "RaahiApp/1.0 (contact@raahi.com)")

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var opResp OverpassResponse
	if err := json.NewDecoder(resp.Body).Decode(&opResp); err != nil {
		return nil, err
	}

	var landmarks []Landmark
	for _, el := range opResp.Elements {
		// Try to get a meaningful name
		name := el.Tags["name"]
		if name == "" {
			name = el.Tags["name:en"]
		}
		if name == "" {
			// Skip unnamed landmarks, as they are not useful for typical recommendations
			continue
		}

		// Node coordinates vs Way/Relation center coordinates
		lLat := el.Lat
		lLon := el.Lon
		if el.Type == "way" || el.Type == "relation" {
			lLat = el.Center.Lat
			lLon = el.Center.Lon
		}

		lType := ""
		if v, ok := el.Tags["amenity"]; ok {
			lType = v
		} else if v, ok := el.Tags["tourism"]; ok {
			lType = v
		} else if v, ok := el.Tags["shop"]; ok {
			lType = fmt.Sprintf("shop_%s", v)
		} else if v, ok := el.Tags["historic"]; ok {
			lType = fmt.Sprintf("historic_%s", v)
		}

		dist := HaversineKm(lat, lon, lLat, lLon) * 1000.0

		landmarks = append(landmarks, Landmark{
			Name:      name,
			Type:      lType,
			Lat:       lLat,
			Lon:       lLon,
			DistanceM: math.Round(dist), // round to nearest meter
		})
	}

	cacheMutex.Lock()
	discoveryCache[cacheKey] = cacheEntry{data: landmarks, timestamp: time.Now()}
	// Basic cache pruning: if it grows too large, clear oldest (simple clear here)
	if len(discoveryCache) > 1000 {
		discoveryCache = make(map[string]cacheEntry)
	}
	cacheMutex.Unlock()

	return landmarks, nil
}

func GetVillagesAlongRoute(coords [][2]float64) ([]Landmark, error) {
	if len(coords) == 0 {
		return []Landmark{}, nil
	}

	// Build a stable cache key from sample of coords
	keySamples := []string{}
	for i := 0; i < len(coords); i += len(coords)/5 + 1 {
		keySamples = append(keySamples, fmt.Sprintf("%.4f,%.4f", coords[i][0], coords[i][1]))
	}
	cacheKey := "villages:" + strings.Join(keySamples, "|")

	cacheMutex.RLock()
	if entry, ok := discoveryCache[cacheKey]; ok {
		if time.Since(entry.timestamp) < cacheExpiration {
			cacheMutex.RUnlock()
			return entry.data.([]Landmark), nil
		}
	}
	cacheMutex.RUnlock()

	// 1. Pick sample points every few KM to build the Overpass search union
	var aroundQueries []string
	step := 1
	if len(coords) > 20 {
		step = len(coords) / 8 // Sample ~8 points along the path to keep Overpass queries fast and light
	}
	for i := 0; i < len(coords); i += step {
		aroundQueries = append(aroundQueries, fmt.Sprintf(`node(around:1500, %f, %f)[place~"^(village|town|hamlet|suburb)$"];`, coords[i][0], coords[i][1]))
	}
	// Always include the last point
	last := coords[len(coords)-1]
	aroundQueries = append(aroundQueries, fmt.Sprintf(`node(around:1500, %f, %f)[place~"^(village|town|hamlet|suburb)$"];`, last[0], last[1]))

	unionStr := strings.Join(aroundQueries, "\n  ")

	// 2. Build Overpass query with union of around results
	// Set query execution limit on Overpass server side to 5s to avoid blocking their server if stuck
	query := fmt.Sprintf(`[out:json][timeout:5];
(
  %s
);
out body;`, unionStr)

	apiURL := "https://overpass-api.de/api/interpreter"
	data := url.Values{}
	data.Set("data", query)

	req, _ := http.NewRequest("POST", apiURL, strings.NewReader(data.Encode()))
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "RaahiApp/1.0 (contact@raahi.com)")

	// Short client timeout to fail fast if Overpass is overloaded/down
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var opResp OverpassResponse
	if err := json.NewDecoder(resp.Body).Decode(&opResp); err != nil {
		return nil, err
	}

	var villages []Landmark
	seen := make(map[string]bool)
	for _, el := range opResp.Elements {
		name := el.Tags["name"]
		if name == "" {
			name = el.Tags["name:hi"] // try Hindi if English name missing
		}
		if name == "" {
			continue
		}

		// Deduplicate
		normName := strings.ToLower(strings.TrimSpace(name))
		if seen[normName] {
			continue
		}
		seen[normName] = true

		villages = append(villages, Landmark{
			Name: name,
			Type: el.Tags["place"],
			Lat:  el.Lat,
			Lon:  el.Lon,
		})
	}

	cacheMutex.Lock()
	discoveryCache[cacheKey] = cacheEntry{data: villages, timestamp: time.Now()}
	cacheMutex.Unlock()

	return villages, nil
}
