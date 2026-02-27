package config

import "os"

// Config holds application configuration from environment
type Config struct {
	WhatsAppMode    string // "listener" | "automatic"
	SlackKey        string // OAuth token — when set, QR is sent to Slack
	SlackChannel    string // Channel for QR (e.g. whatsapp-bot)
	MongoURI        string // MongoDB connection URI (MONGO_URI or MONGODB_URI)
	MongoDB         string // MongoDB database name
	AvenaWebhookURL string // Avena webhook for records — when set, group_join sends to Avena
}

// Load reads configuration from environment variables
func Load() *Config {
	return &Config{
		WhatsAppMode:    getEnv("WHATSAPP_MODE", "listener"),
		SlackKey:        getEnv("SLACK_KEY", getEnv("SLACK_OAUTH_ACCESS_TOKEN", "")),
		SlackChannel:    getEnv("SLACK_CHANNEL", "whatsapp-bot"),
		MongoURI:        getEnv("MONGO_URI", getEnv("MONGODB_URI", "")),
		MongoDB:         getEnv("MONGO_DB", "whatsbot"),
		AvenaWebhookURL: getEnv("AVENA_WEBHOOK_URL", "https://avena-bot.appspot.com/webhook/communitiesWhatsapp"),
	}
}

// IsListenerMode returns true when the bot should listen for messages and group_join events
func (c *Config) IsListenerMode() bool {
	return c.WhatsAppMode != "automatic"
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
