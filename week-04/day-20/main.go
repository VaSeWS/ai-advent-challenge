package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const telegramURL = "https://api.telegram.org"

func telegramSend(ctx context.Context, client *http.Client, endpoint, token, chatID, text string) error {
	chunks := splitTelegramMessages(text)
	for i, chunk := range chunks {
		if err := telegramSendChunk(ctx, client, endpoint, token, chatID, chunk); err != nil {
			return fmt.Errorf("Telegram delivered %d of %d message chunks: %w", i, len(chunks), err)
		}
	}
	return nil
}

func splitTelegramMessages(text string) []string {
	const maxCharacters = 4096
	if text == "" {
		return []string{""}
	}
	chunks := make([]string, 0, (len(text)+maxCharacters-1)/maxCharacters)
	start := 0
	characters := 0
	for byteIndex, r := range text {
		width := utf16Width(r)
		if characters+width > maxCharacters {
			chunks = append(chunks, text[start:byteIndex])
			start = byteIndex
			characters = 0
		}
		characters += width
	}
	chunks = append(chunks, text[start:])
	return chunks
}

func utf16Width(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}

func telegramSendChunk(ctx context.Context, client *http.Client, endpoint, token, chatID, text string) error {
	form := url.Values{"chat_id": {chatID}, "text": {text}}
	requestURL := strings.TrimRight(endpoint, "/") + "/bot" + url.PathEscape(token) + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, strings.NewReader(form.Encode()))
	if err != nil {
		return errors.New("create Telegram request failed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("send Telegram request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Telegram returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return fmt.Errorf("decode Telegram response: %w", err)
	}
	if !result.OK {
		return errors.New("Telegram API rejected the message")
	}
	return nil
}


type sendMessageInput struct {
	Text string `json:"text" jsonschema:"Message text to send"`
}

type sendMessageOutput struct {
	Sent bool `json:"sent"`
}

func runTelegramServer(ctx context.Context) error {
	token, chatID := os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID")
	if token == "" || chatID == "" {
		return errors.New("TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID must be set")
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "telegram-notifications", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "send_message", Description: "Send a text notification to the configured Telegram chat."}, func(ctx context.Context, _ *mcp.CallToolRequest, input sendMessageInput) (*mcp.CallToolResult, sendMessageOutput, error) {
		if strings.TrimSpace(input.Text) == "" {
			return nil, sendMessageOutput{}, errors.New("message text is required")
		}
		if err := telegramSend(ctx, http.DefaultClient, telegramURL, token, chatID, input.Text); err != nil {
			return nil, sendMessageOutput{}, err
		}
		return nil, sendMessageOutput{Sent: true}, nil
	})
	return server.Run(ctx, &mcp.StdioTransport{})
}

func main() {
	if len(os.Args) != 2 || os.Args[1] != "telegram-server" {
		fmt.Fprintln(os.Stderr, "usage: go run ./week-04/day-20 telegram-server")
		os.Exit(1)
	}
	if err := runTelegramServer(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
