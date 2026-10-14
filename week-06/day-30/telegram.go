package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	telegramAPI               = "https://api.telegram.org"
	telegramMessageUTF16Limit = 4096
	maxHistoryPairs           = 8
	maxHistoryBytes           = 48 << 10
)

var errTelegramDelivery = errors.New("Telegram delivery failed")

// The LAN self-call carries private text and a bearer; never send it to a proxy.
// Capture the standard transport once, independently of later default-client changes.
var telegramLocalTransport = func() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return transport
}()

type telegramDeliveryError struct {
	delivered int
	total     int
}

func (e *telegramDeliveryError) Error() string {
	return fmt.Sprintf("Telegram delivered %d of %d message chunks", e.delivered, e.total)
}

func (e *telegramDeliveryError) Unwrap() error {
	return errTelegramDelivery
}

type telegramClient struct {
	token        string
	chatID       string
	serviceURL   string
	serviceToken string
	http         *http.Client
	localHTTP    *http.Client
	history      []chatMessage
}
type telegramUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Chat struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
		Text string `json:"text"`
	} `json:"message"`
}

func runTelegram(ctx context.Context, cfg config, ready chan<- error, serveReady <-chan struct{}) error {
	client := &telegramClient{token: cfg.telegramToken, chatID: cfg.telegramChatID,
		serviceURL: "http://" + cfg.listen, serviceToken: cfg.serviceToken,
		http: &http.Client{Timeout: 40 * time.Second, CheckRedirect: rejectRedirects}, localHTTP: &http.Client{Transport: telegramLocalTransport, Timeout: 2 * time.Hour, CheckRedirect: rejectRedirects},
		history: []chatMessage{{Role: "system", Content: "You are a helpful assistant. Answer clearly and honestly."}}}
	if err := client.verifyWebhook(ctx); err != nil {
		ready <- err
		return err
	}
	var offset int64
	started := false
	for {
		updates, err := client.getUpdates(ctx, offset)
		if err != nil {
			if !started {
				ready <- err
				started = true
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if !started {
			ready <- nil
			started = true
			select {
			case <-serveReady:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		for _, update := range updates {
			if err := client.processUpdate(ctx, update); err != nil {
				return err
			}
			offset = update.UpdateID + 1
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func (c *telegramClient) verifyWebhook(ctx context.Context) error {
	var result struct {
		URL string `json:"url"`
	}
	if err := c.telegramRequest(ctx, "getWebhookInfo", nil, &result); err != nil {
		return errors.New("cannot verify Telegram webhook state; polling was not started")
	}
	if result.URL != "" {
		return errors.New("Telegram webhook is configured; refusing to remove it or start polling")
	}
	return nil
}

func (c *telegramClient) getUpdates(ctx context.Context, offset int64) ([]telegramUpdate, error) {
	values := url.Values{"timeout": {"30"}}
	if offset > 0 {
		values.Set("offset", strconv.FormatInt(offset, 10))
	}
	var updates []telegramUpdate
	if err := c.telegramRequest(ctx, "getUpdates", values, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

func (c *telegramClient) processUpdate(ctx context.Context, update telegramUpdate) error {
	if update.Message == nil {
		return nil
	}
	if update.Message.Chat.Type != "private" || strconv.FormatInt(update.Message.Chat.ID, 10) != c.chatID {
		return nil
	}
	text := strings.TrimSpace(update.Message.Text)
	if text == "" {
		return nil
	}
	switch text {
	case "/start", "/start@aiadventsvswsbot":
		return c.sendTelegram(ctx, "Send a text message to chat with the local model. Use /reset to clear this process's conversation. History is kept in memory only until this service stops.")
	case "/reset", "/reset@aiadventsvswsbot":
		c.history = c.history[:1]
		return c.sendTelegram(ctx, "Conversation history cleared.")
	}
	if len(text) > maxMessageBytes {
		return c.sendTelegram(ctx, "Message is too long; the limit is 8192 UTF-8 bytes.")
	}
	messages := c.messagesFor(text)
	answer, err := c.localCompletion(ctx, messages)
	if err != nil {
		return err
	}
	if answer == "" {
		return errors.New("local model returned an empty response")
	}
	if err := c.sendTelegram(ctx, answer); err != nil {
		return err
	}
	// Commit only after the full response was delivered.
	c.history = append(c.history, chatMessage{Role: "user", Content: text}, chatMessage{Role: "assistant", Content: answer})
	c.pruneHistory()
	return nil
}

func (c *telegramClient) messagesFor(current string) []chatMessage {
	messages := append([]chatMessage(nil), c.history...)
	messages = append(messages, chatMessage{Role: "user", Content: current})
	for len(messages) > 3 && (len(messages) > 1+2*maxHistoryPairs+1 || encodedMessageSize(messages) > maxHistoryBytes || !messagesWithinMessageLimit(messages)) {
		messages = append(messages[:1], messages[3:]...)
	}
	return messages
}

func (c *telegramClient) pruneHistory() {
	for len(c.history) >= 3 && (len(c.history) > 1+2*maxHistoryPairs || encodedMessageSize(c.history) > maxHistoryBytes || !messagesWithinMessageLimit(c.history)) {
		c.history = append(c.history[:1], c.history[3:]...)
	}
}

func messagesWithinMessageLimit(messages []chatMessage) bool {
	for _, message := range messages {
		if len(message.Content) > maxMessageBytes {
			return false
		}
	}
	return true
}

func encodedMessageSize(messages []chatMessage) int {
	data, _ := json.Marshal(messages)
	return len(data)
}

func (c *telegramClient) localCompletion(ctx context.Context, messages []chatMessage) (string, error) {
	body, err := json.Marshal(struct {
		Model     string        `json:"model"`
		Messages  []chatMessage `json:"messages"`
		MaxTokens int           `json:"max_tokens"`
	}{"local", messages, defaultOutputTokens})
	if err != nil {
		return "", errors.New("encode local request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serviceURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", errors.New("create local request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.serviceToken)
	response, err := c.localHTTP.Do(request)
	if err != nil {
		return "", errors.New("local chat service request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return "", fmt.Errorf("local chat service returned HTTP %d", response.StatusCode)
	}
	var result chatResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, requestBodyLimit)).Decode(&result); err != nil {
		return "", errors.New("invalid local chat response")
	}
	if len(result.Choices) != 1 || result.Choices[0].Message.Role != "assistant" || (result.Choices[0].FinishReason != "stop" && result.Choices[0].FinishReason != "length") || result.Choices[0].Message.Content == "" {
		return "", errors.New("local chat service has no supported assistant answer")
	}
	return result.Choices[0].Message.Content, nil
}

func (c *telegramClient) sendTelegram(ctx context.Context, text string) error {
	chunks := splitTelegramChunks(text)
	for i, chunk := range chunks {
		values := url.Values{"chat_id": {c.chatID}, "text": {chunk}}
		if err := c.telegramRequest(ctx, "sendMessage", values, nil); err != nil {
			return &telegramDeliveryError{delivered: i, total: len(chunks)}
		}
	}
	return nil
}

func splitTelegramChunks(text string) []string {
	if text == "" {
		return []string{""}
	}
	chunks := make([]string, 0, len(text)/telegramMessageUTF16Limit+1)
	var chunk strings.Builder
	units := 0
	for _, r := range text {
		n := 1
		if r > 0xFFFF {
			n = 2
		}
		if units+n > telegramMessageUTF16Limit {
			chunks = append(chunks, chunk.String())
			chunk.Reset()
			units = 0
		}
		chunk.WriteRune(r)
		units += n
	}
	if chunk.Len() > 0 {
		chunks = append(chunks, chunk.String())
	}
	return chunks
}

func (c *telegramClient) telegramRequest(ctx context.Context, method string, values url.Values, output any) error {
	endpoint := telegramAPI + "/bot" + url.PathEscape(c.token) + "/" + method
	var body io.Reader
	requestURL := endpoint
	if values != nil {
		if method == "getUpdates" {
			requestURL += "?" + values.Encode()
		} else {
			body = strings.NewReader(values.Encode())
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, body)
	if err != nil {
		return errors.New("create Telegram request failed")
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return errors.New("Telegram request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return fmt.Errorf("Telegram returned HTTP %d", response.StatusCode)
	}
	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope); err != nil {
		return errors.New("invalid Telegram response")
	}
	if !envelope.OK {
		return errors.New("Telegram API rejected the request")
	}
	if output != nil && json.Unmarshal(envelope.Result, output) != nil {
		return errors.New("invalid Telegram API result")
	}
	return nil
}
