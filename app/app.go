package app

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"

	_ "github.com/mattn/go-sqlite3"
	"github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"

	"whats-bot/config"
	"whats-bot/controller"
	"whats-bot/dao"
	"whats-bot/repository"
	"whats-bot/server"
	"whats-bot/service"
)

// Run bootstraps and runs the application
func Run() {
	cfg := config.Load()
	log.Printf("WhatsApp mode: %s", cfg.WhatsAppMode)

	port := flag.String("port", getEnv("PORT", "8080"), "HTTP server port (or PORT env)")
	storePath := flag.String("store", getEnv("STORE_PATH", "store.db"), "Path to SQLite store (or STORE_PATH env)")
	flag.Parse()

	storeDSN := "file:" + *storePath + "?_foreign_keys=on"
	ctx := context.Background()
	container, err := sqlstore.New(ctx, "sqlite3", storeDSN, nil)
	if err != nil {
		panic(err)
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		panic(err)
	}

	client := whatsmeow.NewClient(deviceStore, nil)
	groups := make(map[string]string)
	groupsMu := sync.RWMutex{}

	avenaDao := dao.NewAvenaDao(cfg.AvenaWebhookURL)
	var msgRepo repository.MessageRepository
	if cfg.MongoURI != "" {
		mongoRepo, err := repository.NewMongoRepository(ctx, cfg.MongoURI, cfg.MongoDB)
		if err != nil {
			log.Fatalf("MongoDB connection failed: %v", err)
		}
		msgRepo = mongoRepo
	}
	whatsappService := service.NewWhatsAppService(client, groups, &groupsMu, avenaDao, msgRepo)

	var reconnectMu sync.Mutex
	var addEventHandlers func(*whatsmeow.Client)
	addEventHandlers = func(c *whatsmeow.Client) {
		// Add message listener BEFORE connecting so we receive events from the start
		if cfg.IsListenerMode() {
			whatsappService.RegisterEventListener()
		}
		c.AddEventHandler(func(evt interface{}) {
			switch evt.(type) {
			case *events.Connected:
				whatsappService.ClearQRCode()
				whatsappService.RefreshGroups(context.Background())
			case *events.LoggedOut:
				whatsappService.SetNeedsQR(true)
				_ = avenaDao.ReportError()
				c.Disconnect()
				go startReconnect(ctx, container, whatsappService, addEventHandlers, &reconnectMu)
			}
		})
	}
	addEventHandlers(client)

	onReconnect := func() {
		whatsappService.Disconnect()
		startReconnect(ctx, container, whatsappService, addEventHandlers, &reconnectMu)
	}

	var slackService *service.SlackService
	if cfg.SlackKey != "" && cfg.SlackChannel != "" {
		slackService = service.NewSlackService(cfg.SlackKey, cfg.SlackChannel)
	}

	whatsappController := controller.NewWhatsAppController(whatsappService, slackService, msgRepo, onReconnect)

	connectWhatsApp(ctx, client, whatsappService)
	defer client.Disconnect()

	mux := http.NewServeMux()
	whatsappMux := http.NewServeMux()
	whatsappController.RegisterRoutes(whatsappMux)
	mux.Handle("/whatsapp/", http.StripPrefix("/whatsapp", whatsappMux))

	server.Run(":"+*port, mux)
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// startReconnect runs when LoggedOut fires: gets new device, creates client, connects with QR
func startReconnect(ctx context.Context, container *sqlstore.Container, whatsappService *service.WhatsAppService, addEventHandlers func(*whatsmeow.Client), reconnectMu *sync.Mutex) {
	reconnectMu.Lock()
	defer reconnectMu.Unlock()

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		log.Printf("Failed to get device for reconnect: %v", err)
		return
	}

	client := whatsmeow.NewClient(deviceStore, nil)
	whatsappService.SetClient(client)
	addEventHandlers(client)

	connectWhatsApp(ctx, client, whatsappService)
}

func connectWhatsApp(ctx context.Context, client *whatsmeow.Client, whatsappService *service.WhatsAppService) {
	if client.Store.ID == nil {
		whatsappService.SetNeedsQR(true)
		qrChan, _ := client.GetQRChannel(ctx)
		if err := client.Connect(); err != nil {
			panic(err)
		}
		go func() {
			for evt := range qrChan {
				if evt.Event == "code" {
					whatsappService.SetQRCode(evt.Code)
					fmt.Println("Scan this QR code with WhatsApp (or GET /whatsapp/qr):")
					if q, err := qrcode.New(evt.Code, qrcode.Medium); err == nil {
						fmt.Println(q.ToSmallString(false))
					}
				} else {
					log.Println("QR code scanned, logged in!")
					break
				}
			}
		}()
	} else {
		if err := client.Connect(); err != nil {
			panic(err)
		}
	}
}
