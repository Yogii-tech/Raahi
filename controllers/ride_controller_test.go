package controllers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"raahi-backend/config"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestCreateRide(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	payload := map[string]interface{}{
		"pickup":        "Delhi",
		"dropoff":       "Gurgaon",
		"seatsTotal":    4,
		"pricePerSeat":  500,
		"vehicleModel":  "Honda",
		"vehicleNumber": "DL123",
	}

	mt.Run("successful ride creation", func(mt *mtest.T) {
		rideCollection = mt.Coll
		config.Database = mt.DB
		// Dummy users collection override to avoid crash on user Name fetch in CreateRide
		// Wait, config.Database.Collection("users") is hardcoded in CreateRide!
		// However, mtest isolates logic. We can mock standard responses anyway.
		
		mt.AddMockResponses(
			mtest.CreateCursorResponse(1, "raahi.users", mtest.FirstBatch, bson.D{{Key: "name", Value: "Chaman"}}), // Fetch driver Name
			mtest.CreateSuccessResponse(), // DeleteMany
			mtest.CreateSuccessResponse(), // InsertOne
		)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("userId", primitive.NewObjectID())

		b, _ := json.Marshal(payload)
		c.Request, _ = http.NewRequest("POST", "/api/rides/create", bytes.NewBuffer(b))
		c.Request.Header.Set("Content-Type", "application/json")

		CreateRide(c)

		if w.Code != http.StatusCreated && w.Code != http.StatusOK {
			t.Errorf("Expected success, got %v: %v", w.Code, w.Body.String())
		}
	})

	mt.Run("invalid seats negative validation", func(mt *mtest.T) {
		rideCollection = mt.Coll

		invalidPayload := map[string]interface{}{
			"pickup":        "Delhi",
			"dropoff":       "Gurgaon",
			"seatsTotal":    0, // INVALID!
			"pricePerSeat":  500,
		}

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("userId", primitive.NewObjectID())

		b, _ := json.Marshal(invalidPayload)
		c.Request, _ = http.NewRequest("POST", "/api/rides/create", bytes.NewBuffer(b))
		c.Request.Header.Set("Content-Type", "application/json")

		CreateRide(c)

		if w.Code != http.StatusBadRequest {
			t.Errorf("Expected 400 Bad Request, got %d", w.Code)
		}
	})
}

func TestBookRide(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	payload := map[string]interface{}{
		"seatsRequested": 2,
		"seatLayout":     []int{1, 2},
		"roofCarrier":    false,
		"motionSickness": false,
	}

	mt.Run("successful booking", func(mt *mtest.T) {
		rideCollection = mt.Coll
		bookingCollection = mt.Coll

		driverId := primitive.NewObjectID()
		passengerId := primitive.NewObjectID() // Different ID

		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, "raahi.rides", mtest.FirstBatch, bson.D{
				{Key: "driverId", Value: driverId},
				{Key: "seatsTotal", Value: int32(4)},
			}), // 1. fetch the Ride
			mtest.CreateCursorResponse(0, "raahi.bookings", mtest.FirstBatch), // 2. getTakenSeats (0 taken)
			mtest.CreateSuccessResponse(),                                     // 3. insert Booking
		)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("userId", passengerId)
		c.Params = []gin.Param{{Key: "rideId", Value: primitive.NewObjectID().Hex()}}

		b, _ := json.Marshal(payload)
		c.Request, _ = http.NewRequest("POST", "/api/rides/book/someid", bytes.NewBuffer(b))
		c.Request.Header.Set("Content-Type", "application/json")

		BookRide(c)

		if w.Code != http.StatusOK && w.Code != http.StatusCreated {
			mt.Fatalf("Expected Success, got %d: %s", w.Code, w.Body.String())
		}
	})

	mt.Run("error cannot book own ride", func(mt *mtest.T) {
		rideCollection = mt.Coll

		driverId := primitive.NewObjectID()

		mt.AddMockResponses(mtest.CreateCursorResponse(0, "raahi.rides", mtest.FirstBatch, bson.D{
			{Key: "driverId", Value: driverId},
		})) // Ride fetch says driver is exactly the user requesting it

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("userId", driverId) // Matches the mock!
		c.Params = []gin.Param{{Key: "rideId", Value: primitive.NewObjectID().Hex()}}

		b, _ := json.Marshal(payload)
		c.Request, _ = http.NewRequest("POST", "/api/rides/book/someid", bytes.NewBuffer(b))

		BookRide(c)

		if w.Code != http.StatusForbidden {
			mt.Fatalf("Expected 403 Forbidden, got %d: %s", w.Code, w.Body.String())
		}
	})

	mt.Run("error overbooking logic", func(mt *mtest.T) {
		rideCollection = mt.Coll
		bookingCollection = mt.Coll

		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, "raahi.rides", mtest.FirstBatch, bson.D{
				{Key: "driverId", Value: primitive.NewObjectID()},
				{Key: "seatsTotal", Value: int32(4)},
			}), // Total Seats: 4
			mtest.CreateCursorResponse(0, "raahi.bookings", mtest.FirstBatch, bson.D{
				{Key: "seatsRequested", Value: int32(3)},
				{Key: "seatLayout", Value: bson.A{int32(1), int32(2), int32(3)}},
			}), // getTakenSeats shows 3 seats are ALREADY taken!
		)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("userId", primitive.NewObjectID())
		c.Params = []gin.Param{{Key: "rideId", Value: primitive.NewObjectID().Hex()}}

		// Payload is requesting 2 seats meaning 3+2 = 5 > 4!
		b, _ := json.Marshal(payload)
		c.Request, _ = http.NewRequest("POST", "/api/rides/book/someid", bytes.NewBuffer(b))

		BookRide(c)

		if w.Code != http.StatusBadRequest {
			mt.Fatalf("Expected 400 Bad Request for overbooking, got %d: %s", w.Code, w.Body.String())
		}
	})
}

func TestUpdateBookingStatus(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	payloadAccept := map[string]interface{}{"status": "accepted"}
	payloadReject := map[string]interface{}{"status": "rejected"}

	mt.Run("successful accept updates ride seats", func(mt *mtest.T) {
		rideCollection = mt.Coll
		bookingCollection = mt.Coll

		driverId := primitive.NewObjectID()
		rideId := primitive.NewObjectID()

		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, "raahi.bookings", mtest.FirstBatch, bson.D{
				{Key: "rideId", Value: rideId},
				{Key: "seatsRequested", Value: int32(2)},
			}), // 1. fetch Booking
			mtest.CreateCursorResponse(0, "raahi.rides", mtest.FirstBatch, bson.D{
				{Key: "_id", Value: rideId},
				{Key: "driverId", Value: driverId},
			}), // 2. fetch Ride (Ownership PASS)
			mtest.CreateSuccessResponse(), // 3. Update Booking Status
			mtest.CreateSuccessResponse(), // 4. Update Ride seatsBooked (increment)
		)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("userId", driverId)
		c.Params = []gin.Param{{Key: "bookingId", Value: primitive.NewObjectID().Hex()}}

		b, _ := json.Marshal(payloadAccept)
		c.Request, _ = http.NewRequest("PUT", "/api/rides/update-booking/someid", bytes.NewBuffer(b))

		UpdateBookingStatus(c)

		if w.Code != http.StatusOK {
			mt.Fatalf("Expected 200 OK for accept, got %d: %s", w.Code, w.Body.String())
		}
	})

	mt.Run("successful reject halts without incrementing ride seats", func(mt *mtest.T) {
		rideCollection = mt.Coll
		bookingCollection = mt.Coll

		driverId := primitive.NewObjectID()
		rideId := primitive.NewObjectID()

		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, "raahi.bookings", mtest.FirstBatch, bson.D{
				{Key: "rideId", Value: rideId},
			}), // 1. fetch Booking
			mtest.CreateCursorResponse(0, "raahi.rides", mtest.FirstBatch, bson.D{
				{Key: "_id", Value: rideId},
				{Key: "driverId", Value: driverId},
			}), // 2. fetch Ride (Ownership PASS)
			mtest.CreateSuccessResponse(), // 3. Update Booking Status
			// NOTE: No 4th response needed, ride seatsBooked shouldn't be touched on rejection
		)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("userId", driverId)
		c.Params = []gin.Param{{Key: "bookingId", Value: primitive.NewObjectID().Hex()}}

		b, _ := json.Marshal(payloadReject)
		c.Request, _ = http.NewRequest("PUT", "/api/rides/update-booking/someid", bytes.NewBuffer(b))

		UpdateBookingStatus(c)

		if w.Code != http.StatusOK {
			mt.Fatalf("Expected 200 OK for reject, got %d: %s", w.Code, w.Body.String())
		}
	})

	mt.Run("forbidden mismatching driver blocks action", func(mt *mtest.T) {
		rideCollection = mt.Coll
		bookingCollection = mt.Coll

		driverId := primitive.NewObjectID()
		maliciousDriverId := primitive.NewObjectID() // Attempting to hijack
		rideId := primitive.NewObjectID()

		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, "raahi.bookings", mtest.FirstBatch, bson.D{
				{Key: "rideId", Value: rideId},
			}), // 1. fetch Booking
			mtest.CreateCursorResponse(0, "raahi.rides", mtest.FirstBatch, bson.D{
				{Key: "_id", Value: rideId},
				{Key: "driverId", Value: driverId}, // Real Driver!
			}), // 2. fetch Ride
		)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Set("userId", maliciousDriverId) // The JWT identity
		c.Params = []gin.Param{{Key: "bookingId", Value: primitive.NewObjectID().Hex()}}

		b, _ := json.Marshal(payloadAccept)
		c.Request, _ = http.NewRequest("PUT", "/api/rides/update-booking/someid", bytes.NewBuffer(b))

		UpdateBookingStatus(c)

		if w.Code != http.StatusForbidden {
			mt.Fatalf("Expected 403 Forbidden for hijacked driver ID, got %d", w.Code)
		}
	})
}

