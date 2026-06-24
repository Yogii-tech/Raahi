//go:build ignore

package main

import (
	"fmt"
	"raahi-backend/utils"
)

func main() {
	lat1, lon1, err1 := utils.Geocode("Bageshwar")
	lat2, lon2, err2 := utils.Geocode("Gangolihat")
	if err1 != nil || err2 != nil {
		fmt.Printf("Geocode error: %v, %v\n", err1, err2)
		return
	}
	fmt.Printf("Bageshwar: %f, %f\n", lat1, lon1)
	fmt.Printf("Gangolihat: %f, %f\n", lat2, lon2)
	_, dist, err := utils.GetRouteFromOSRM(lat1, lon1, lat2, lon2)
	if err != nil {
		fmt.Printf("OSRM error: %v\n", err)
		return
	}
	fmt.Printf("Distance (OSRM): %f m (%.2f km)\n", dist, dist/1000)
	haversine := utils.HaversineKm(lat1, lon1, lat2, lon2)
	fmt.Printf("Distance (Haversine): %.2f km\n", haversine)
}
