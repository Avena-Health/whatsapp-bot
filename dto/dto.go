package dto

// SendMessageDto matches the NestJS SendMessageDto for API compatibility
type SendMessageDto struct {
	Community   string  `json:"community"`
	Message     *string `json:"message"`
	AppendixURL *string `json:"appendixUrl"`
	FileType    *string `json:"fileType"`
}

// SendMessageResponse is the success response for send-message
type SendMessageResponse struct {
	OK bool `json:"ok"`
}

// ErrorResponse is the error response for API errors
type ErrorResponse struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason"`
}

// RecordDto matches the NestJS RecordDto for Avena webhook compatibility
type RecordDto struct {
	GroupName string `json:"groupName,omitempty"`
	Lada      string `json:"lada"`
	Phone     string `json:"phone"`
	Date      string `json:"date"`
}
