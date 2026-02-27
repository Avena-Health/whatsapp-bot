package controller

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/skip2/go-qrcode"
	"whats-bot/dto"
	"whats-bot/repository"
	"whats-bot/service"
)

// WhatsAppController handles HTTP requests for WhatsApp operations
type WhatsAppController struct {
	whatsappService *service.WhatsAppService
	slackService    *service.SlackService
	msgRepo         repository.MessageRepository
	onReconnect     func()
}

// NewWhatsAppController creates a new WhatsAppController.
func NewWhatsAppController(whatsappService *service.WhatsAppService, slackService *service.SlackService, msgRepo repository.MessageRepository, onReconnect func()) *WhatsAppController {
	return &WhatsAppController{
		whatsappService: whatsappService,
		slackService:    slackService,
		msgRepo:         msgRepo,
		onReconnect:     onReconnect,
	}
}

// RegisterRoutes registers the controller's routes on the given mux
func (c *WhatsAppController) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /send-message", c.HandleSendMessage)
	mux.HandleFunc("GET /qr", c.HandleGetQR)
	mux.HandleFunc("GET /messages", c.HandleGetMessages)
}

// HandleGetQR returns the current QR code when pairing is needed
func (c *WhatsAppController) HandleGetQR(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Connected: session is ready
	if c.whatsappService.IsLoggedIn() && c.whatsappService.IsConnected() {
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "connected"})
		return
	}

	// Need pairing: trigger reconnect if no QR yet
	code, needsPairing := c.whatsappService.GetQRCode()
	if !needsPairing || code == "" {
		if c.onReconnect != nil {
			go c.onReconnect()
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "waiting", "message": "Reconnecting. Poll again for QR."})
		return
	}

	// Have QR code
	q, err := qrcode.New(code, qrcode.Medium)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "error", "message": "Failed to generate QR"})
		return
	}

	// Send to Slack when configured (chat.postMessage + catbox for image URL)
	if c.slackService != nil {
		png, pngErr := q.PNG(256)
		if pngErr != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "error", "message": "Failed to generate QR image"})
			return
		}
		if sendErr := c.slackService.SendQRImage(png); sendErr != nil {
			log.Printf("Slack upload failed: %v", sendErr)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  "pairing",
				"message": "Slack upload failed: " + sendErr.Error(),
				"code":    code,
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "pairing", "message": "QR sent to Slack"})
		return
	}

	// No Slack: return code only
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "pairing",
		"code":   code,
	})
}

// HandleSendMessage handles POST /send-message
func (c *WhatsAppController) HandleSendMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !c.whatsappService.IsSessionValid() {
		writeError(w, "WhatsApp session not connected. Scan QR code at GET /whatsapp/qr", http.StatusServiceUnavailable)
		return
	}

	var body dto.SendMessageDto
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	result := c.whatsappService.SendMessage(body)
	if !result.OK {
		status := http.StatusInternalServerError
		if result.Reason == "Community not found" || result.Reason == "Either message or appendixUrl must be provided" {
			status = http.StatusBadRequest
		}
		writeError(w, result.Reason, status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(dto.SendMessageResponse{OK: true})
}

// HandleGetMessages returns messages filtered by startDate and endDate query params
func (c *WhatsAppController) HandleGetMessages(w http.ResponseWriter, r *http.Request) {
	if c.msgRepo == nil {
		writeError(w, "Messages not available (MongoDB not configured)", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	startStr := r.URL.Query().Get("startDate")
	endStr := r.URL.Query().Get("endDate")

	var start, end *time.Time
	if startStr != "" {
		t, err := time.Parse(time.RFC3339, startStr)
		if err != nil {
			t, err = time.Parse("2006-01-02", startStr)
			if err != nil {
				writeError(w, "Invalid startDate", http.StatusBadRequest)
				return
			}
		}
		start = &t
	}
	if endStr != "" {
		t, err := time.Parse(time.RFC3339, endStr)
		if err != nil {
			t, err = time.Parse("2006-01-02", endStr)
			if err != nil {
				writeError(w, "Invalid endDate", http.StatusBadRequest)
				return
			}
		}
		t = t.Add(24*time.Hour - time.Nanosecond) // end of day
		end = &t
	}

	messages, err := c.msgRepo.FindMessages(r.Context(), start, end)
	if err != nil {
		log.Printf("Error finding messages: %v", err)
		writeError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(messages)
}

func writeError(w http.ResponseWriter, reason string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(dto.ErrorResponse{OK: false, Reason: reason})
}
