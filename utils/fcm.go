package utils

import (
	"context"
	"log"
	"os"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"google.golang.org/api/option"
)

var fcmClient *messaging.Client

// InitFCM initializes the Firebase Admin SDK using the service account credentials file.
// Set FIREBASE_CREDENTIALS_FILE env var to the path of your serviceAccountKey.json.
// If the variable is empty, FCM is silently disabled (pushes will be skipped).
func InitFCM() {
	credFile := os.Getenv("FIREBASE_CREDENTIALS_FILE")
	if credFile == "" {
		log.Println("[FCM] FIREBASE_CREDENTIALS_FILE not set — push notifications disabled")
		return
	}

	opt := option.WithCredentialsFile(credFile)
	app, err := firebase.NewApp(context.Background(), nil, opt)
	if err != nil {
		log.Printf("[FCM] Failed to initialize Firebase app: %v", err)
		return
	}

	client, err := app.Messaging(context.Background())
	if err != nil {
		log.Printf("[FCM] Failed to get FCM client: %v", err)
		return
	}

	fcmClient = client
	log.Println("[FCM] Firebase Cloud Messaging initialized successfully")
}

// SendPushNotification sends a push notification to a single FCM device token.
// The notification block makes the OS display it on the lock screen even when the app is closed.
// The data block carries extra key-value pairs for deep-linking inside the app.
// If fcmToken is empty or FCM is not initialized, this is a no-op.
func SendPushNotification(fcmToken, title, body string, data map[string]string) {
	if fcmClient == nil || fcmToken == "" {
		return
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
				Sound:       "default",
				ClickAction: "FLUTTER_NOTIFICATION_CLICK",
			},
		},
		APNS: &messaging.APNSConfig{
			Payload: &messaging.APNSPayload{
				Aps: &messaging.Aps{
					Sound: "default",
					Badge: intPtr(1),
				},
			},
		},
	}

	ctx := context.Background()
	resp, err := fcmClient.Send(ctx, msg)
	if err != nil {
		log.Printf("[FCM] Failed to send push: %v", err)
		return
	}
	log.Printf("[FCM] Push sent successfully: %s", resp)
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
			Payload: &messaging.APNSPayload{
				Aps: &messaging.Aps{
					Sound: "default",
					Badge: intPtr(1),
				},
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
}

func intPtr(i int) *int {
	return &i
}
