package service

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"whats-bot/dao"
	"whats-bot/dto"
	"whats-bot/repository"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// WhatsAppService handles WhatsApp messaging business logic
type WhatsAppService struct {
	client   *whatsmeow.Client
	groups   map[string]string
	groupsMu *sync.RWMutex

	qrCode  string
	qrMu    sync.RWMutex
	needsQR bool

	avenaDao dao.AvenaDao                 // optional: sends group_join records to Avena webhook
	msgRepo  repository.MessageRepository // optional: saves messages to MongoDB in listener mode
}

// SetQRCode stores the current QR code (called during pairing)
func (s *WhatsAppService) SetQRCode(code string) {
	s.qrMu.Lock()
	s.qrCode = code
	s.qrMu.Unlock()
	if s.avenaDao != nil {
		_ = s.avenaDao.ReportError()
	}
}

// GetQRCode returns the current QR code and whether we're waiting for scan
func (s *WhatsAppService) GetQRCode() (code string, needsPairing bool) {
	s.qrMu.RLock()
	defer s.qrMu.RUnlock()
	return s.qrCode, s.needsQR
}

// SetNeedsQR sets whether the client needs to show QR (pairing mode)
func (s *WhatsAppService) SetNeedsQR(needs bool) {
	s.qrMu.Lock()
	defer s.qrMu.Unlock()
	s.needsQR = needs
	if !needs {
		s.qrCode = ""
	}
}

// ClearQRCode clears the stored QR (after successful scan)
func (s *WhatsAppService) ClearQRCode() {
	s.SetNeedsQR(false)
}

// IsSessionValid returns true if the WhatsApp session exists and is ready to send messages
func (s *WhatsAppService) IsSessionValid() bool {
	return s.client != nil && s.client.Store != nil && s.client.Store.ID != nil
}

// IsLoggedIn returns true if the client is connected and authenticated (not logged out/unlinked)
func (s *WhatsAppService) IsLoggedIn() bool {
	return s.client != nil && s.client.IsLoggedIn()
}

// IsConnected returns true if the websocket is connected (not just authenticated)
func (s *WhatsAppService) IsConnected() bool {
	return s.client != nil && s.client.IsConnected()
}

// SetClient replaces the WhatsApp client (used when reconnecting after LoggedOut)
func (s *WhatsAppService) SetClient(client *whatsmeow.Client) {
	s.client = client
}

// Disconnect disconnects the current client
func (s *WhatsAppService) Disconnect() {
	if s.client != nil {
		s.client.Disconnect()
	}
}

// NewWhatsAppService creates a new WhatsAppService
func NewWhatsAppService(client *whatsmeow.Client, groups map[string]string, groupsMu *sync.RWMutex, avenaDao dao.AvenaDao, msgRepo repository.MessageRepository) *WhatsAppService {
	return &WhatsAppService{
		client:   client,
		groups:   groups,
		groupsMu: groupsMu,
		avenaDao: avenaDao,
		msgRepo:  msgRepo,
	}
}

// SendResult represents the result of a send operation
type SendResult struct {
	OK     bool
	Reason string
}

// RefreshGroups fetches and updates the groups list from WhatsApp
func (s *WhatsAppService) RefreshGroups(ctx context.Context) {
	groupList, err := s.client.GetJoinedGroups(ctx)
	if err != nil {
		log.Printf("Failed to refresh groups: %v", err)
		return
	}
	s.groupsMu.Lock()
	defer s.groupsMu.Unlock()
	for k := range s.groups {
		delete(s.groups, k)
	}
	fmt.Println("\n=== Groups (community name → JID) ===")
	for _, g := range groupList {
		s.groups[g.Name] = g.JID.String()
		communityInfo := ""
		if g.IsParent {
			communityInfo = " [COMMUNITY]"
		} else if !g.LinkedParentJID.IsEmpty() {
			communityInfo = " [sub-group]"
		}
		fmt.Printf("  %s → %s%s\n", g.Name, g.JID, communityInfo)
	}
	fmt.Printf("\nTotal: %d groups\n\n", len(s.groups))
}

// SendMessage sends a message to a community (text, image, PDF, or video URL)
func (s *WhatsAppService) SendMessage(dto dto.SendMessageDto) SendResult {
	community := dto.Community
	message := ptrToStr(dto.Message)
	appendixURL := ptrToStr(dto.AppendixURL)
	fileType := ptrToStr(dto.FileType)

	s.groupsMu.RLock()
	groupId, found := s.groups[community]
	s.groupsMu.RUnlock()

	if !found {
		log.Printf("Community not found: %s", community)
		return SendResult{OK: false, Reason: "Community not found"}
	}

	if message == "" && appendixURL == "" {
		return SendResult{OK: false, Reason: "Either message or appendixUrl must be provided"}
	}

	jid, err := types.ParseJID(groupId)
	if err != nil {
		log.Printf("Invalid group JID: %v", err)
		return SendResult{OK: false, Reason: "Invalid group"}
	}

	ctx := context.Background()

	// Video: send text with URL concatenated
	if fileType == "video" && appendixURL != "" {
		videoMessage := message
		if message != "" {
			videoMessage = message + " " + appendixURL
		} else {
			videoMessage = appendixURL
		}
		msg := &waE2E.Message{Conversation: proto.String(videoMessage)}
		if _, err := s.client.SendMessage(ctx, jid, msg); err != nil {
			log.Printf("Failed to send video message: %v", err)
			return SendResult{OK: false, Reason: "Failed to send message"}
		}
		log.Printf("Video URL message sent to %s", community)
		return SendResult{OK: true}
	}

	// PDF or image: download, upload, send with caption
	if appendixURL != "" && fileType != "" && fileType != "video" {
		downloadURL := convertGoogleDriveURL(appendixURL)
		var data []byte
		var filename, mimeType string

		if fileType == "pdf" {
			data, filename = s.downloadFile(downloadURL)
			if data == nil {
				return SendResult{OK: false, Reason: "Failed to download file"}
			}
			// If filename from headers is generic, try to get real name from Google Drive page or URL
			if filename == "" || filename == "document.pdf" {
				if strings.Contains(appendixURL, "drive.google.com") {
					driveName := getGoogleDriveFileName(appendixURL)
					if driveName != "" && driveName != "document.pdf" {
						filename = driveName
					}
				}
				// Fallback: extract from URL path (e.g. .../documento.pdf)
				if filename == "" || filename == "document.pdf" {
					if u, err := url.Parse(appendixURL); err == nil {
						path := strings.TrimPrefix(u.Path, "/")
						if parts := strings.Split(path, "/"); len(parts) > 0 {
							last := parts[len(parts)-1]
							if strings.Contains(last, ".") && len(last) > 4 {
								filename = last
							}
						}
					}
				}
			}
			if !strings.HasSuffix(filename, ".pdf") {
				filename = filename + ".pdf"
			}
			mimeType = "application/pdf"
		} else {
			data, filename, mimeType = s.downloadImage(downloadURL)
			if data == nil {
				return SendResult{OK: false, Reason: "Failed to download image"}
			}
		}

		if fileType == "pdf" {
			resp, err := s.client.Upload(ctx, data, whatsmeow.MediaDocument)
			if err != nil {
				log.Printf("Failed to upload document: %v", err)
				return SendResult{OK: false, Reason: "Failed to upload document"}
			}
			docMsg := &waE2E.DocumentMessage{
				URL:           &resp.URL,
				DirectPath:    &resp.DirectPath,
				MediaKey:      resp.MediaKey,
				FileEncSHA256: resp.FileEncSHA256,
				FileSHA256:    resp.FileSHA256,
				FileLength:    &resp.FileLength,
				Title:         proto.String(filename),
				FileName:      proto.String(filename),
				Mimetype:      proto.String(mimeType),
			}
			if message != "" {
				docMsg.Caption = proto.String(message)
			}
			msg := &waE2E.Message{DocumentMessage: docMsg}
			if _, err := s.client.SendMessage(ctx, jid, msg); err != nil {
				log.Printf("Failed to send document: %v", err)
				return SendResult{OK: false, Reason: "Failed to send message"}
			}
			log.Printf("PDF sent to %s", community)
		} else {
			resp, err := s.client.Upload(ctx, data, whatsmeow.MediaImage)
			if err != nil {
				log.Printf("Failed to upload image: %v", err)
				return SendResult{OK: false, Reason: "Failed to upload image"}
			}
			imgMsg := &waE2E.ImageMessage{
				URL:           &resp.URL,
				DirectPath:    &resp.DirectPath,
				MediaKey:      resp.MediaKey,
				FileEncSHA256: resp.FileEncSHA256,
				FileSHA256:    resp.FileSHA256,
				FileLength:    &resp.FileLength,
				Mimetype:      proto.String(mimeType),
			}
			if message != "" {
				imgMsg.Caption = proto.String(message)
			}
			msg := &waE2E.Message{ImageMessage: imgMsg}
			if _, err := s.client.SendMessage(ctx, jid, msg); err != nil {
				log.Printf("Failed to send image: %v", err)
				return SendResult{OK: false, Reason: "Failed to send message"}
			}
			log.Printf("Image sent to %s", community)
		}
		return SendResult{OK: true}
	}

	// Text only
	if message != "" {
		msg := &waE2E.Message{Conversation: proto.String(message)}
		if _, err := s.client.SendMessage(ctx, jid, msg); err != nil {
			log.Printf("Failed to send message: %v", err)
			return SendResult{OK: false, Reason: "Failed to send message"}
		}
		log.Printf("Message sent to %s", community)
		return SendResult{OK: true}
	}

	return SendResult{OK: true}
}

// RegisterEventListener registers handlers for messages and group_join events
func (s *WhatsAppService) RegisterEventListener() {
	log.Printf("📡 Event listener registered (messages, group_join)")
	s.client.AddEventHandler(func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Message:
			s.handleIncomingMessage(v)
		case *events.GroupInfo:
			s.handleGroupInfo(v)
		case *events.JoinedGroup:
			s.handleJoinedGroup(v)
		}
	})
}

func (s *WhatsAppService) handleIncomingMessage(evt *events.Message) {
	// Debug: log every message received
	chatStr := evt.Info.Chat.String()
	isGroup := evt.Info.Chat.Server == types.GroupServer
	isFromMe := evt.Info.IsFromMe
	text := getMessageText(evt.Message)
	log.Printf("📥 Message received: chat=%s isGroup=%v isFromMe=%v textLen=%d", chatStr, isGroup, isFromMe, len(text))

	if evt.Info.Chat.Server != types.GroupServer {
		log.Printf("⏭️ Ignored: not a group (server=%s)", evt.Info.Chat.Server)
		return
	}
	if evt.Info.IsFromMe {
		log.Printf("⏭️ Ignored: message from self")
		return
	}
	if strings.TrimSpace(text) == "" {
		log.Printf("⏭️ Ignored: empty text")
		return
	}
	groupName := s.getGroupName(evt.Info.Chat.String())
	sender := evt.Info.Sender.String()
	if evt.Info.Sender.Server == types.HiddenUserServer {
		sender = evt.Info.Sender.User + "@lid"
	}
	log.Printf("📩 Message from group \"%s\" by %s: %s", groupName, sender, text)

	if s.msgRepo != nil {
		msgID := evt.Info.ID
		ts := evt.Info.Timestamp
		if ts.IsZero() {
			ts = time.Now()
		}
		doc := &repository.MessageDoc{
			WhatsAppID: msgID,
			Text:       text,
			Phone:      sender,
			Timestamp:  ts,
			GroupName:  groupName,
		}
		if err := s.msgRepo.SaveMessage(context.Background(), doc); err != nil {
			log.Printf("Error saving message to MongoDB: %v", err)
		} else {
			log.Printf("✅ Mensaje guardado en la base de datos: group=%q phone=%s text=%q", doc.GroupName, doc.Phone, doc.Text)
		}
	}
}

func (s *WhatsAppService) handleGroupInfo(evt *events.GroupInfo) {
	if len(evt.Join) == 0 {
		return
	}
	groupName := s.getGroupName(evt.JID.String())
	for _, jid := range evt.Join {
		participant := s.participantToPhone(jid, evt.JID)
		log.Printf("GROUP JOIN 📩 User %s joined group \"%s\" → phone: %s", jid.String(), groupName, participant)
		s.saveRecordToAvena(participant, groupName)
	}
}

func (s *WhatsAppService) handleJoinedGroup(evt *events.JoinedGroup) {
	groupName := evt.GroupName.Name
	var participant string
	if evt.SenderPN != nil && !evt.SenderPN.IsEmpty() {
		participant = evt.SenderPN.User
	} else if evt.Sender != nil {
		participant = s.participantToPhone(*evt.Sender, evt.JID)
	} else {
		participant = "unknown"
	}
	log.Printf("GROUP JOIN 📩 Bot added to group \"%s\" by %s", groupName, participant)
	s.saveRecordToAvena(participant, groupName)
}

// participantToPhone returns the phone number for a participant. For LID users, tries GetPNForLID and GetGroupInfo fallback.
func (s *WhatsAppService) participantToPhone(jid types.JID, groupJID types.JID) string {
	if jid.Server != types.HiddenUserServer {
		return jid.User
	}
	if s.client == nil || s.client.Store == nil || s.client.Store.LIDs == nil {
		return jid.User
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if pn, err := s.client.Store.LIDs.GetPNForLID(ctx, jid); err == nil && !pn.IsEmpty() {
		return pn.User
	}
	if !groupJID.IsEmpty() {
		if _, err := s.client.GetGroupInfo(ctx, groupJID); err == nil {
			if pn, err := s.client.Store.LIDs.GetPNForLID(ctx, jid); err == nil && !pn.IsEmpty() {
				return pn.User
			}
		}
	}
	return jid.User
}

func (s *WhatsAppService) saveRecordToAvena(participant, groupName string) {
	if s.avenaDao == nil {
		return
	}
	lada, phone := parseParticipant(participant)
	record := &dto.RecordDto{
		GroupName: groupName,
		Lada:      lada,
		Phone:     phone,
		Date:      time.Now().Format("2006-01-02T15:04:05Z07:00"),
	}
	if err := s.avenaDao.CreateRecord(record); err != nil {
		log.Printf("Error saving record to Avena: %v", err)
	}
}

var participantRegex = regexp.MustCompile(`^(\d{1,3})?(\d+)(?:@.+)?$`)

func parseParticipant(participant string) (lada, phone string) {
	participant = strings.TrimSpace(participant)
	userPart := regexp.MustCompile(`@.+`).ReplaceAllString(participant, "")

	match := participantRegex.FindStringSubmatch(userPart)
	if match == nil {
		return "", userPart
	}
	return match[1], match[2]
}

func (s *WhatsAppService) getGroupName(chatID string) string {
	s.groupsMu.RLock()
	defer s.groupsMu.RUnlock()
	for name, jid := range s.groups {
		if jid == chatID {
			return name
		}
	}
	return "Unknown group"
}

func getMessageText(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	if msg.Conversation != nil {
		return *msg.Conversation
	}
	if msg.ExtendedTextMessage != nil && msg.ExtendedTextMessage.Text != nil {
		return *msg.ExtendedTextMessage.Text
	}
	return ""
}

func ptrToStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func convertGoogleDriveURL(url string) string {
	if !strings.Contains(url, "drive.google.com") {
		return url
	}
	fileIDMatch := regexp.MustCompile(`/file/d/([a-zA-Z0-9_-]+)`).FindStringSubmatch(url)
	if fileIDMatch == nil {
		fileIDMatch = regexp.MustCompile(`[?&]id=([a-zA-Z0-9_-]+)`).FindStringSubmatch(url)
	}
	if fileIDMatch != nil {
		return "https://drive.google.com/uc?export=download&id=" + fileIDMatch[1]
	}
	return url
}

func extractGoogleDriveFileID(url string) string {
	if !strings.Contains(url, "drive.google.com") {
		return ""
	}
	m := regexp.MustCompile(`/file/d/([a-zA-Z0-9_-]+)`).FindStringSubmatch(url)
	if m == nil {
		m = regexp.MustCompile(`[?&]id=([a-zA-Z0-9_-]+)`).FindStringSubmatch(url)
	}
	if m != nil {
		return m[1]
	}
	return ""
}

func getGoogleDriveFileName(originalURL string) string {
	fileID := extractGoogleDriveFileID(originalURL)
	if fileID == "" {
		return "document.pdf"
	}
	viewURL := "https://drive.google.com/file/d/" + fileID + "/view"
	req, err := http.NewRequest("GET", viewURL, nil)
	if err != nil {
		return "document.pdf"
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0.0.0 Safari/537.36")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "document.pdf"
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "document.pdf"
	}
	// Parse <title>...</title> - format is typically "filename - Google Drive"
	titleMatch := regexp.MustCompile(`(?i)<title>(.*?)</title>`).FindSubmatch(body)
	if titleMatch == nil {
		return "document.pdf"
	}
	title := strings.TrimSpace(string(titleMatch[1]))
	title = regexp.MustCompile(`\s*-\s*Google\s*Drive\s*`).ReplaceAllString(title, "")
	if idx := strings.Index(title, " - "); idx >= 0 {
		title = strings.TrimSpace(title[:idx])
	}
	if title == "" {
		return "document.pdf"
	}
	return title
}

func (s *WhatsAppService) downloadFile(url string) ([]byte, string) {
	httpClient := &http.Client{
		Timeout:   120 * time.Second,
		Transport: &http.Transport{MaxIdleConns: 10, IdleConnTimeout: 90 * time.Second},
	}
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	}

	resp, err := httpClient.Get(url)
	if err != nil {
		log.Printf("Download error: %v", err)
		return nil, ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Download failed: status %d", resp.StatusCode)
		return nil, ""
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Read body error: %v", err)
		return nil, ""
	}

	filename := "document.pdf"
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if m := regexp.MustCompile(`filename[*]?=['"]?([^'";\n]+)`).FindStringSubmatch(cd); m != nil {
			filename = strings.TrimSpace(m[1])
		}
	}

	return data, filename
}

func (s *WhatsAppService) downloadImage(url string) ([]byte, string, string) {
	httpClient := &http.Client{
		Timeout:   60 * time.Second,
		Transport: &http.Transport{MaxIdleConns: 10, IdleConnTimeout: 90 * time.Second},
	}

	resp, err := httpClient.Get(url)
	if err != nil {
		log.Printf("Download image error: %v", err)
		return nil, "", ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Download image failed: status %d", resp.StatusCode)
		return nil, "", ""
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Read image body error: %v", err)
		return nil, "", ""
	}

	mimeType := resp.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "image/jpeg"
	}
	if idx := strings.Index(mimeType, ";"); idx >= 0 {
		mimeType = strings.TrimSpace(mimeType[:idx])
	}

	filename := "image"
	switch {
	case strings.Contains(mimeType, "png"):
		filename = "image.png"
	case strings.Contains(mimeType, "gif"):
		filename = "image.gif"
	case strings.Contains(mimeType, "webp"):
		filename = "image.webp"
	default:
		filename = "image.jpg"
	}

	return data, filename, mimeType
}
