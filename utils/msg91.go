package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

type msg91Payload struct {
	Otp    string `json:"otp"`
	Mobile string `json:"mobile"`
}

// SendOTPviaMSG91 sends a 6-digit OTP to the given Indian mobile number using MSG91.
// The phone number should be 10 digits (without country code).
// Returns an error if the SMS could not be sent.
func SendOTPviaMSG91(phone string, otp string) error {
	authKey := os.Getenv("MSG91_AUTH_KEY")
	templateID := os.Getenv("MSG91_TEMPLATE_ID")

	if authKey == "" || templateID == "" {
		// Fall back to console log if keys are missing (development mode)
		fmt.Printf("\n[MSG91 NOT CONFIGURED] OTP for %s: %s\n\n", phone, otp)
		return nil
	}

	// MSG91 requires mobile in international format (91 + 10 digit number)
	mobile := "91" + phone

	payload := msg91Payload{
		Otp:    otp,
		Mobile: mobile,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal MSG91 payload: %w", err)
	}

	url := fmt.Sprintf("https://control.msg91.com/api/v5/otp?template_id=%s&mobile=%s", templateID, mobile)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create MSG91 request: %w", err)
	}

	req.Header.Set("authkey", authKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send SMS via MSG91: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("MSG91 returned status %d", resp.StatusCode)
	}

	fmt.Printf("[MSG91] OTP sent successfully to +91%s\n", phone)
	return nil
}
