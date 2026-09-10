package utils

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/auth"
	"firebase.google.com/go/v4/messaging"
	"go.mongodb.org/mongo-driver/bson"
	"google.golang.org/api/option"
	"raahi-backend/config"
)

var fcmClient *messaging.Client
var firebaseAuthClient *auth.Client

// InitFCM initializes the Firebase Admin SDK using available credentials.
// It tries the following in order:
// 1. FIREBASE_CREDENTIALS_FILE environment variable (path to JSON file)
// 2. FIREBASE_SERVICE_ACCOUNT_JSON environment variable (raw JSON string)
// 3. "serviceAccountKey.json" in the current working directory
// 4. Google Application Default Credentials (ADC) on Cloud Run/GCP
func InitFCM() {
	var app *firebase.App
	var err error
	ctx := context.Background()

	fbConfig := &firebase.Config{
		ProjectID: "project-4e312d2c-0d4c-4929-860",
	}

	credFile := os.Getenv("FIREBASE_CREDENTIALS_FILE")
	serviceAccountJSON := os.Getenv("FIREBASE_SERVICE_ACCOUNT_JSON")

	if credFile != "" {
		if _, statErr := os.Stat(credFile); statErr == nil {
			opt := option.WithCredentialsFile(credFile)
			app, err = firebase.NewApp(ctx, fbConfig, opt)
			if err == nil {
				log.Printf("[FCM] Initialized using FIREBASE_CREDENTIALS_FILE (%s)", credFile)
			}
		}
	}

	if app == nil && serviceAccountJSON != "" {
		opt := option.WithCredentialsJSON([]byte(serviceAccountJSON))
		app, err = firebase.NewApp(ctx, fbConfig, opt)
		if err == nil {
			log.Println("[FCM] Initialized using FIREBASE_SERVICE_ACCOUNT_JSON env var")
		}
	}

	if app == nil {
		defaultKeyPath := "serviceAccountKey.json"
		if _, statErr := os.Stat(defaultKeyPath); statErr == nil {
			opt := option.WithCredentialsFile(defaultKeyPath)
			app, err = firebase.NewApp(ctx, fbConfig, opt)
			if err == nil {
				log.Println("[FCM] Initialized using local serviceAccountKey.json")
			}
		}
	}

	if app == nil {
		// Fallback to Application Default Credentials (ADC) on Google Cloud Run
		app, err = firebase.NewApp(ctx, fbConfig)
		if err == nil {
			log.Println("[FCM] Initialized using Google Application Default Credentials (ADC)")
		}
	}

	if err != nil || app == nil {
		log.Printf("[FCM] Failed to initialize Firebase app: %v", err)
		return
	}

	client, err := app.Messaging(ctx)
	if err != nil {
		log.Printf("[FCM] Failed to get FCM client: %v", err)
	} else {
		fcmClient = client
		log.Println("[FCM] Firebase Cloud Messaging initialized successfully")
	}

	aClient, aErr := app.Auth(ctx)
	if aErr != nil {
		log.Printf("[Firebase Auth] Failed to get Auth client: %v", aErr)
	} else {
		firebaseAuthClient = aClient
		log.Println("[Firebase Auth] Firebase Auth client initialized successfully")
	}
}

func GetFirebaseAuth() *auth.Client {
	return firebaseAuthClient
}

// removeFCMToken cleans up stale tokens from the database when a user uninstalls the app
func removeFCMToken(token string) {
	if config.Database == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := config.Database.Collection("users").UpdateMany(
		ctx,
		bson.M{"fcm_token": token},
		bson.M{"$unset": bson.M{"fcm_token": ""}},
	)
	if err != nil {
		log.Printf("[FCM] Failed to remove stale token from DB: %v", err)
	} else {
		log.Printf("[FCM] Successfully removed stale token from DB")
	}
}

// SendPushNotification sends a push notification to a single FCM device token.
// The notification block makes the OS display it on the lock screen even when the app is closed.
// The data block carries extra key-value pairs for deep-linking inside the app.
// If fcmToken is empty or FCM is not initialized, this is a no-op.
func SendPushNotification(fcmToken, title, body string, data map[string]string) error {
	if fcmClient == nil {
		log.Printf("[FCM] Error: FCM client is nil!")
		return fmt.Errorf("FCM client is not initialized on server")
	}
	if fcmToken == "" {
		return fmt.Errorf("FCM token is empty")
	}

	msg := &messaging.Message{
		Token: fcmToken,
		Notification: &messaging.Notification{
			Title: title,
			Body:  body,
		},
		Data: data,
		Android: &messaging.AndroidConfig{
			Priority: "high",
			Notification: &messaging.AndroidNotification{
				Sound: "default",
			},
		},
		APNS: &messaging.APNSConfig{
			Headers: map[string]string{
				"apns-priority":  "10",
				"apns-push-type": "alert",
			},
			Payload: &messaging.APNSPayload{
				Aps: &messaging.Aps{
					Alert: &messaging.ApsAlert{
						Title: title,
						Body:  body,
					},
					Sound:            "default",
					Badge:            intPtr(1),
					MutableContent:   true,
					ContentAvailable: true,
				},
			},
		},
		Webpush: &messaging.WebpushConfig{
			Headers: map[string]string{
				"Urgency": "high",
				"TTL":     "86400",
			},
			Notification: &messaging.WebpushNotification{
				Title:              title,
				Body:               body,
				RequireInteraction: true,
				Icon:               "/logo192.png",
				Badge:              "/logo192.png",
			},
			FCMOptions: &messaging.WebpushFCMOptions{
				Link: "/",
			},
		},
	}

	ctx := context.Background()
	var err error
	var resp string

	for attempt := 1; attempt <= 2; attempt++ {
		resp, err = fcmClient.Send(ctx, msg)
		if err == nil {
			log.Printf("[FCM] Push sent successfully: %s", resp)
			return nil
		}
		
		if messaging.IsUnregistered(err) {
			log.Printf("[FCM] Token is unregistered. Cleaning up DB...")
			go removeFCMToken(fcmToken)
			return fmt.Errorf("FCM token is unregistered/invalid: %v", err)
		}

		if messaging.IsUnavailable(err) || messaging.IsInternal(err) {
			log.Printf("[FCM] Transient error on attempt %d: %v. Retrying...", attempt, err)
			time.Sleep(500 * time.Millisecond)
			continue
		}

		break
	}

	log.Printf("[FCM] Failed to send push after retries: %v", err)
	return err
}

// SendMulticastPush sends the same notification to multiple FCM tokens (e.g. admin broadcast).
func SendMulticastPush(tokens []string, title, body string, data map[string]string) {
	if fcmClient == nil || len(tokens) == 0 {
		return
	}

	// Filter out empty tokens
	validTokens := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if t != "" {
			validTokens = append(validTokens, t)
		}
	}
	if len(validTokens) == 0 {
		return
	}

	msg := &messaging.MulticastMessage{
		Tokens: validTokens,
		Notification: &messaging.Notification{
			Title: title,
			Body:  body,
		},
		Data: data,
		Android: &messaging.AndroidConfig{
			Priority: "high",
			Notification: &messaging.AndroidNotification{
				Sound: "default",
			},
		},
		APNS: &messaging.APNSConfig{
			Headers: map[string]string{
				"apns-priority":  "10",
				"apns-push-type": "alert",
			},
			Payload: &messaging.APNSPayload{
				Aps: &messaging.Aps{
					Alert: &messaging.ApsAlert{
						Title: title,
						Body:  body,
					},
					Sound:            "default",
					Badge:            intPtr(1),
					MutableContent:   true,
					ContentAvailable: true,
				},
			},
		},
		Webpush: &messaging.WebpushConfig{
			Headers: map[string]string{
				"Urgency": "high",
				"TTL":     "86400",
			},
			Notification: &messaging.WebpushNotification{
				Title:              title,
				Body:               body,
				RequireInteraction: true,
				Icon:               "/logo192.png",
				Badge:              "/logo192.png",
			},
			FCMOptions: &messaging.WebpushFCMOptions{
				Link: "/",
			},
		},
	}

	ctx := context.Background()
	resp, err := fcmClient.SendEachForMulticast(ctx, msg)
	if err != nil {
		log.Printf("[FCM] Multicast push failed: %v", err)
		return
	}
	log.Printf("[FCM] Multicast push: %d success, %d failure", resp.SuccessCount, resp.FailureCount)

	// Clean up any stale tokens in bulk
	if resp.FailureCount > 0 {
		var staleTokens []string
		for i, response := range resp.Responses {
			if response.Error != nil && messaging.IsUnregistered(response.Error) {
				staleTokens = append(staleTokens, validTokens[i])
			}
		}

		if len(staleTokens) > 0 {
			go func(tokensToRemove []string) {
				if config.Database == nil {
					return
				}
				ctxCleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()

				_, cleanupErr := config.Database.Collection("users").UpdateMany(
					ctxCleanup,
					bson.M{"fcm_token": bson.M{"$in": tokensToRemove}},
					bson.M{"$unset": bson.M{"fcm_token": ""}},
				)
				if cleanupErr != nil {
					log.Printf("[FCM] Failed to bulk remove stale tokens: %v", cleanupErr)
				} else {
					log.Printf("[FCM] Successfully removed %d stale tokens", len(tokensToRemove))
				}
			}(staleTokens)
		}
	}
}

func intPtr(i int) *int {
	return &i
}
