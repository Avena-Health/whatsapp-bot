package dao

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"whats-bot/dto"
)

const defaultAPIURL = "https://avena-bot.appspot.com/webhook/communitiesWhatsapp"

// AvenaDao sends records and error reports to the Avena webhook
type AvenaDao interface {
	CreateRecord(record *dto.RecordDto) error
	ReportError() error
}

// AvenaDaoImpl implements AvenaDao via HTTP
type AvenaDaoImpl struct {
	apiURL    string
	client    *http.Client
	skipLocal bool
}

// NewAvenaDao creates an AvenaDao that POSTs to the webhook
func NewAvenaDao(apiURL string) AvenaDao {
	if apiURL == "" {
		apiURL = defaultAPIURL
	}
	return &AvenaDaoImpl{
		apiURL: apiURL,
		client: &http.Client{Timeout: 15 * time.Second},
		skipLocal: true,
	}
}

// CreateRecord POSTs the record to the Avena webhook
func (d *AvenaDaoImpl) CreateRecord(record *dto.RecordDto) error {
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	resp, err := d.client.Post(d.apiURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	log.Printf("SUCCESS SAVE RECORD: %s", resp.Status)
	return nil
}

// ReportError sends a warning to the webhook; skips when running on localhost
func (d *AvenaDaoImpl) ReportError() error {
	if d.skipLocal && isLocalhost() {
		return nil
	}
	url := d.apiURL + "/warning-error"
	resp, err := d.client.Post(url, "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func isLocalhost() bool {
	hostname, _ := os.Hostname()
	h := strings.ToLower(hostname)
	return h == "localhost" || strings.Contains(h, "local") || h == "127.0.0.1"
}
