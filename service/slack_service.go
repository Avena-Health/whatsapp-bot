package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
)

// SlackService sends notifications to Slack via chat.postMessage (same as Java backend)
type SlackService struct {
	token   string
	channel string
}

// NewSlackService creates a SlackService.
func NewSlackService(token, channel string) *SlackService {
	return &SlackService{token: token, channel: channel}
}

// uploadToCatbox uploads PNG to catbox.moe and returns the public URL
func uploadToCatbox(png []byte) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("fileToUpload", "whatsapp-qr.png")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(png); err != nil {
		return "", err
	}
	_ = w.WriteField("reqtype", "fileupload")
	if err := w.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", "https://catbox.moe/user/api.php", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	url := strings.TrimSpace(string(body))
	if url == "" || !strings.HasPrefix(url, "http") {
		return "", fmt.Errorf("catbox upload failed")
	}
	return url, nil
}

// SendQRImage uploads QR PNG to catbox, then sends to Slack via chat.postMessage.
func (s *SlackService) SendQRImage(png []byte) error {
	if s.token == "" || s.channel == "" {
		return fmt.Errorf("slack token or channel not configured")
	}

	imageURL, err := uploadToCatbox(png)
	if err != nil {
		return fmt.Errorf("upload image: %w", err)
	}

	payload := map[string]interface{}{
		"channel": s.channel,
		"text":    "WhatsApp QR - Scan to pair",
		"attachments": []map[string]interface{}{
			{
				"blocks": []map[string]interface{}{
					{
						"type": "section",
						"text": map[string]interface{}{
							"type": "mrkdwn",
							"text": "Scan this QR code with WhatsApp to link the bot.",
						},
					},
					{"type": "divider"},
					{
						"type":      "image",
						"image_url": imageURL,
						"alt_text":  "WhatsApp QR code",
					},
				},
			},
		},
	}
	jsonBody, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", "https://slack.com/api/chat.postMessage", bytes.NewReader(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(respBody, &result)

	if !result.OK {
		return fmt.Errorf("slack: %s", result.Error)
	}
	return nil
}
