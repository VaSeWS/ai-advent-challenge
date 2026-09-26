package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type forecastInput struct {
	Latitude  float64 `json:"latitude" jsonschema:"Latitude of the location, from -90 to 90"`
	Longitude float64 `json:"longitude" jsonschema:"Longitude of the location, from -180 to 180"`
}

type forecastOutput struct {
	Time        string  `json:"time" jsonschema:"Local time of the weather reading"`
	Temperature float64 `json:"temperature_c" jsonschema:"Air temperature in degrees Celsius"`
	Wind        float64 `json:"wind_kmh" jsonschema:"Wind speed in kilometers per hour"`
}

func forecast(ctx context.Context, _ *mcp.CallToolRequest, in forecastInput) (*mcp.CallToolResult, forecastOutput, error) {
	if in.Latitude < -90 || in.Latitude > 90 || in.Longitude < -180 || in.Longitude > 180 {
		return nil, forecastOutput{}, errors.New("coordinates out of range")
	}
	query := url.Values{
		"latitude":  {strconv.FormatFloat(in.Latitude, 'f', -1, 64)},
		"longitude": {strconv.FormatFloat(in.Longitude, 'f', -1, 64)},
		"current":   {"temperature_2m,wind_speed_10m"},
		"timezone":  {"auto"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.open-meteo.com/v1/forecast?"+query.Encode(), nil)
	if err != nil {
		return nil, forecastOutput{}, err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, forecastOutput{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, forecastOutput{}, fmt.Errorf("Open-Meteo: HTTP %d", resp.StatusCode)
	}
	var data struct {
		Current struct {
			Time        string  `json:"time"`
			Temperature float64 `json:"temperature_2m"`
			Wind        float64 `json:"wind_speed_10m"`
		} `json:"current"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, forecastOutput{}, err
	}
	if data.Current.Time == "" {
		return nil, forecastOutput{}, errors.New("Open-Meteo returned no current weather")
	}
	return nil, forecastOutput{Time: data.Current.Time, Temperature: data.Current.Temperature, Wind: data.Current.Wind}, nil
}

func run(ctx context.Context, showWeather bool) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "weather-list-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: exec.Command(executable, "server")}, nil)
	if err != nil {
		return fmt.Errorf("connect to local MCP server: %w", err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("list MCP tools: %w", err)
	}
	fmt.Println("Local weather MCP connected. Available tools:")
	for _, tool := range tools.Tools {
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return fmt.Errorf("encode input schema: %w", err)
		}
		fmt.Printf("- %s: %s\n  input: %s\n", tool.Name, tool.Description, schema)
	}
	if !showWeather {
		return nil
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "current_weather", Arguments: map[string]any{
		"latitude": 55.75, "longitude": 37.62,
	}})
	if err != nil {
		return err
	}
	if result.IsError {
		return fmt.Errorf("current_weather: %v", result.Content)
	}
	fmt.Println("Moscow weather:")
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			fmt.Println(text.Text)
		}
	}
	return nil
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "server" {
		server := mcp.NewServer(&mcp.Implementation{Name: "local-weather", Version: "1.0.0"}, nil)
		mcp.AddTool(server, &mcp.Tool{Name: "current_weather", Description: "Get current air temperature and wind speed at latitude and longitude from Open-Meteo."}, forecast)
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			log.Fatal(err)
		}
		return
	}
	weather := flag.Bool("weather", false, "also call the weather tool for Moscow")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := run(ctx, *weather); err != nil {
		log.Fatal(err)
	}
}
