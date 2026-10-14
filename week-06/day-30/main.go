package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	pinnedModelID  = "mlx-community/Qwen3.5-4B-4bit"
	pinnedRevision = "0e7ffd5c629ef7719d4cbc04069232580bfa9d9c"
	backendURL     = "http://127.0.0.1:8080/v1/chat/completions"
	contextLimit   = 16384
)

func rejectRedirects(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

type config struct {
	listen         string
	loopback       bool
	backendModel   string
	serviceToken   string
	telegramToken  string
	telegramChatID string
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Printf("service stopped: %v", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("day30", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	listen := flags.String("listen", "", "private LAN IPv4 address:port (default: discover private interface); use -dev-loopback for loopback")
	loopback := flags.Bool("dev-loopback", false, "allow explicit loopback-only development binding")
	showHelp := flags.Bool("help", false, "show usage and manual environment/run instructions")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("invalid command options; use -help")
	}
	if *showHelp {
		printHelp(os.Stdout)
		return nil
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments; use -help")
	}

	serviceToken := os.Getenv("DAY30_SERVICE_TOKEN")
	if len(serviceToken) < 32 {
		return errors.New("DAY30_SERVICE_TOKEN must contain at least 32 characters")
	}
	modelPath, err := pinnedSnapshotPath()
	if err != nil {
		return err
	}
	listenAddr, err := selectListenAddress(*listen, *loopback)
	if err != nil {
		return err
	}
	telegramToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	telegramChatID := os.Getenv("TELEGRAM_CHAT_ID")
	if (telegramToken == "") != (telegramChatID == "") {
		return errors.New("set both TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID, or neither")
	}
	if telegramChatID != "" {
		id, err := strconv.ParseInt(telegramChatID, 10, 64)
		if err != nil || id <= 0 {
			return errors.New("TELEGRAM_CHAT_ID must be the configured positive private-chat ID")
		}
	}
	cfg := config{listen: listenAddr, loopback: *loopback, backendModel: modelPath, serviceToken: serviceToken,
		telegramToken: telegramToken, telegramChatID: telegramChatID}

	if err := checkBackendHealth(context.Background(), modelPath); err != nil {
		return err
	}
	if cfg.telegramToken != "" {
		preflight := &telegramClient{token: cfg.telegramToken, http: &http.Client{Timeout: 40 * time.Second, CheckRedirect: rejectRedirects}}
		if err := preflight.verifyWebhook(context.Background()); err != nil {
			return err
		}
	}
	api := newService(cfg)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", api.health)
	mux.HandleFunc("GET /v1/limits", api.limits)
	mux.HandleFunc("POST /v1/chat/completions", api.chatCompletions)
	server := &http.Server{Addr: cfg.listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 2 * time.Hour, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancelRun := context.WithCancel(signalCtx)
	defer cancelRun()
	listener, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		return errors.New("HTTP listener could not be opened")
	}

	var telegramDone <-chan error
	var serveReady chan struct{}
	if cfg.telegramToken != "" {
		serveReady = make(chan struct{})
		ready := make(chan error, 1)
		result := make(chan error, 1)
		telegramDone = result
		go func() { result <- runTelegram(ctx, cfg, ready, serveReady) }()
		select {
		case err := <-ready:
			if err != nil {
				listener.Close()
				cancelRun()
				return errors.New("Telegram polling preflight failed; HTTP service was not started")
			}
		case <-signalCtx.Done():
			listener.Close()
			cancelRun()
			return context.Canceled
		}
	}

	log.Printf("private chat service listening on %s; HTTP is plain-text on the trusted LAN only", cfg.listen)
	return superviseHTTP(signalCtx, cancelRun, server, listener, telegramDone, serveReady)
}

func superviseHTTP(ctx context.Context, cancel context.CancelFunc, server *http.Server, listener net.Listener, telegramDone <-chan error, serveReady chan struct{}) error {
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	if serveReady != nil {
		close(serveReady)
	}

	var terminalErr error
	serveFinished := false
	telegramFinished := false
	select {
	case err := <-serveResult:
		serveFinished = true
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			terminalErr = errors.New("HTTP listener failed")
		}
	case err := <-telegramDone:
		telegramFinished = true
		if !errors.Is(err, context.Canceled) && ctx.Err() == nil {
			var deliveryErr *telegramDeliveryError
			if errors.As(err, &deliveryErr) {
				terminalErr = deliveryErr
			} else {
				terminalErr = errors.New("Telegram polling stopped unexpectedly")
			}
		}
	case <-ctx.Done():
	}

	cancel()
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), generationTimeout+15*time.Second)
	defer cancelShutdown()
	shutdownErr := server.Shutdown(shutdown)
	if shutdownErr != nil {
		// Do not release process ownership while an active handler may still be draining.
		_ = server.Shutdown(context.Background())
	}
	if !serveFinished {
		if err := <-serveResult; err != nil && !errors.Is(err, http.ErrServerClosed) {
			terminalErr = errors.New("HTTP listener failed")
		}
	}
	if telegramDone != nil && !telegramFinished {
		<-telegramDone
	}
	if shutdownErr != nil {
		return errors.New("HTTP server shutdown did not complete cleanly")
	}
	if terminalErr != nil {
		return terminalErr
	}
	return nil
}

func pinnedSnapshotPath() (string, error) {
	root := os.Getenv("HF_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("cannot resolve model cache location")
		}
		root = filepath.Join(home, ".cache", "huggingface")
	}
	return filepath.Join(root, "hub", "models--mlx-community--Qwen3.5-4B-4bit", "snapshots", pinnedRevision), nil
}

func selectListenAddress(explicit string, allowLoopback bool) (string, error) {
	if explicit == "" {
		if allowLoopback {
			return "127.0.0.1:8090", nil
		}
		ip, err := discoverPrivateIPv4()
		if err != nil {
			return "", err
		}
		return net.JoinHostPort(ip.String(), "8090"), nil
	}
	host, port, err := net.SplitHostPort(explicit)
	if err != nil {
		return "", errors.New("-listen must be an IPv4 address and port")
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil {
		return "", errors.New("-listen accepts IPv4 addresses only")
	}
	if ip.IsLoopback() {
		if !allowLoopback {
			return "", errors.New("loopback binding requires -dev-loopback")
		}
	} else if !isPrivateLANIPv4(ip) {
		return "", errors.New("-listen must be a private LAN IPv4 address")
	}
	portNum, err := net.LookupPort("tcp", port)
	if err != nil || portNum < 1 || portNum > 65535 {
		return "", errors.New("-listen port must be between 1 and 65535")
	}
	return net.JoinHostPort(ip.String(), port), nil
}

func isPrivateLANIPv4(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil || v4.IsLoopback() || v4.IsLinkLocalUnicast() {
		return false
	}
	return v4[0] == 10 || v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31 || v4[0] == 192 && v4[1] == 168
}

func discoverPrivateIPv4() (net.IP, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, errors.New("cannot inspect network interfaces")
	}
	var candidates []net.IP
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			var ip net.IP
			switch v := address.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if isPrivateLANIPv4(ip) {
				candidates = append(candidates, append(net.IP(nil), ip.To4()...))
			}
		}
	}
	if len(candidates) == 0 {
		return nil, errors.New("no private LAN IPv4 interface found; specify -listen or use -dev-loopback")
	}
	if len(candidates) > 1 {
		return nil, errors.New("multiple private LAN IPv4 addresses found; choose one explicitly with -listen")
	}
	return candidates[0], nil
}

func printHelp(w io.Writer) {
	fmt.Fprintln(w, `Day 30 local chat service (stdlib only)

Environment (never pass credentials as flags):
  DAY30_SERVICE_TOKEN   required strong bearer token (32+ characters)
  TELEGRAM_BOT_TOKEN    optional; set together with TELEGRAM_CHAT_ID
  TELEGRAM_CHAT_ID      existing configured private chat only
  HF_HOME              optional Hugging Face cache root; defaults to ~/.cache/huggingface

Commands from repository root (credentials already provided privately in the environment):
  go run ./week-06/day-30
  # explicit LAN binding: go run ./week-06/day-30 -listen 192.168.1.20:8090
  # development only: go run ./week-06/day-30 -dev-loopback
  # stop with Ctrl-C (or SIGTERM); no launch agent is installed

Use a fresh random service token from a password manager. Provision tokens privately through hidden input or a secure environment mechanism with shell tracing disabled; never type secret assignments into shell history or put credentials in arguments, URLs, logs, or recordings. HTTP is unencrypted: use only a trusted private LAN. Do not expose port 8090 to the internet. MLX must already be running on 127.0.0.1:8080 with the pinned model, thinking enabled, and MAX_KV_SIZE=16384. Telegram polling begins only if both Telegram variables are set; startup checks for a webhook but never removes it. This process does not load .env files.`)
}

func bearerMatches(header, expected string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got := header[len(prefix):]
	if len(got) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, limit int64, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
