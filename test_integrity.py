import requests
import json
import time

BASE_URL = "http://localhost:8080/api"

def test_flows():
    print("--- 1. Testing Auth & Security ---")
    # Send OTP
    r = requests.post(f"{BASE_URL}/auth/otp/send", json={"phone_number": "9876543210"})
    print(f"Send OTP: {r.status_code} - {r.json().get('message')}")
    
    # Verify OTP
    r = requests.post(f"{BASE_URL}/auth/otp/verify", json={"phone_number": "9876543210", "otp": "123456"})
    token = r.json().get("token")
    user_id = r.json().get("user", {}).get("id")
    print(f"Verify OTP: {r.status_code} - Token received: {token[:10]}...")
    
    headers = {"Authorization": f"Bearer {token}"}

    print("\n--- 2. Testing Admin Interface ---")
    # Promote to Admin
    r = requests.post(f"{BASE_URL}/auth/promote-admin", json={"secret_key": "RAAHI_ADMIN_2026"}, headers=headers)
    print(f"Promote Admin: {r.status_code} - {r.json().get('message')}")
    
    # Fetch Stats
    r = requests.get(f"{BASE_URL}/admin/stats", headers=headers)
    print(f"Admin Stats: {r.status_code}")
    if r.status_code == 200:
        stats = r.json()
        print(f"  Rides: {stats['rides']['total']}, Parcels: {stats['parcels']['total']}, Users: {stats['users']['total']}")

    # Fetch User Registry
    r = requests.get(f"{BASE_URL}/admin/users", headers=headers)
    print(f"Admin User Registry: {r.status_code} - {len(r.json())} users found")

    print("\n--- 3. Testing Driver Interface ---")
    # Create a Ride (Parallel Geocoding Test)
    ride_data = {
        "pickup": "Kotdwar",
        "dropoff": "Lansdowne",
        "vehicleModel": "Innova",
        "vehicleNumber": "UK-12-3456",
        "departureTime": "10:00 AM",
        "date": "2026-06-20",
        "seatsTotal": 6,
        "pricePerSeat": 500
    }
    start_time = time.time()
    r = requests.post(f"{BASE_URL}/rides/create", json=ride_data, headers=headers)
    duration = time.time() - start_time
    print(f"Create Ride: {r.status_code} - {r.json().get('message')} (Duration: {duration:.2f}s)")

    print("\n--- 4. Testing Passenger Interface ---")
    # Search for Rides (Index & Regex Test)
    r = requests.get(f"{BASE_URL}/rides/available?pickup=Kotdwar&dropoff=Lansdowne", headers=headers)
    rides = r.json()
    print(f"Search Rides: {r.status_code} - {len(rides)} matches found")

    # Book a Parcel (Modern Parcel Flow)
    if len(rides) > 0:
        ride_id = rides[0]["id"]
        parcel_data = {
            "type": "parcel",
            "pickup": "Kotdwar",
            "dropoff": "Lansdowne",
            "parcelSize": "Medium",
            "recipientName": "Rahul Singh",
            "contactNumber": "9988776655",
            "price": "350"
        }
        r = requests.post(f"{BASE_URL}/rides/{ride_id}/book", json=parcel_data, headers=headers)
        print(f"Book Parcel: {r.status_code} - {r.json().get('message')}")

    print("\n--- 5. Testing Security (Revocation) ---")
    # Logout (Revokes Session)
    r = requests.post(f"{BASE_URL}/user/logout", headers=headers)
    print(f"Logout: {r.status_code} - {r.json().get('message')}")
    
    # Try using the old token (Should fail)
    r = requests.get(f"{BASE_URL}/user/profile", headers=headers)
    print(f"Old Token Usage: {r.status_code} - {r.json().get('error')}")

if __name__ == "__main__":
    test_flows()
