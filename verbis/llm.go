package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/verbis-ai/verbis/verbis/types"
	"github.com/verbis-ai/verbis/verbis/util"
)

const (
	CustomModelPrefix = "custom-"
	rerankDistPath    = "rerank/rerank"

	MaxNumRerankedChunks      = 3
	RerankNoResultScoreCutoff = 0.2
	RerankSoloScoreCliff      = 0.3

	OllamaHost = "127.0.0.1:11435"
)

var (
	httpClient = &http.Client{
		Timeout: 10 * time.Second,
	}
)

func IsCustomModel(modelName string) bool {
	return strings.HasPrefix(modelName, "custom-")
}

type ModelCreateRequest struct {
	Name      string `json:"name"`
	Modelfile string `json:"modelfile"`
	Stream    bool   `json:"stream"`
}

func createModel(modelName string) error {
	url := fmt.Sprintf("http://%s/api/create", OllamaHost)

	path, err := util.GetDistPath()
	if err != nil {
		return fmt.Errorf("failed to get dist path: %v", err)
	}

	modelFileName := fmt.Sprintf("Modelfile.%s", modelName)
	modelFileData, err := os.ReadFile(filepath.Join(path, modelFileName))
	if err != nil {
		return fmt.Errorf("unable to read modelfile: %v", err)
	}

	log.Printf("Modelfile contents: %s", string(modelFileData))

	payload := ModelCreateRequest{
		Name:      modelName,
		Modelfile: string(modelFileData),
	}
	// Marshal the payload into JSON
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	// Create a new HTTP request
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}

	// Set the appropriate headers
	req.Header.Set("Content-Type", "application/json")

	// Make the HTTP request using the default client
	client := &http.Client{}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	// Read the response body
	responseData, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	log.Printf("Response: %v", string(responseData))
	return nil
}

type StreamResponse struct {
	Model     string            `json:"model"`
	CreatedAt time.Time         `json:"created_at"`
	Message   types.HistoryItem `json:"message"`
	Done      bool              `json:"done"`
}

func chatWithModelStream(ctx context.Context, prompt string, model string, history []types.HistoryItem, resChan chan<- StreamResponse) error {
	url := fmt.Sprintf("http://%s/api/chat", OllamaHost)

	messages := history
	if prompt != "" {
		messages = append(history, types.HistoryItem{
			Role:    "user",
			Content: prompt,
		})
	}

	payload := RequestPayload{
		Model:     model,
		Messages:  messages,
		Stream:    true,
		KeepAlive: KeepAliveTime,
	}

	// Marshal the payload into JSON
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	// Create a new HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}

	// Set the appropriate headers
	req.Header.Set("Content-Type", "application/json")

	// Make the HTTP request using the default client
	client := &http.Client{}
	response, err := client.Do(req)
	if err != nil {
		return err
	}

	// Start a go routine to read from the response body
	go func() {
		defer response.Body.Close()
		reader := bufio.NewReader(response.Body)
		decoder := json.NewDecoder(reader)

		for {
			select {
			case <-ctx.Done():
				fmt.Println("Context cancelled")
				return
			default:
				var streamResp StreamResponse
				if err := decoder.Decode(&streamResp); err == io.EOF {
					break
				} else if err != nil {
					fmt.Println("Error decoding JSON:", err)
					return
				}

				resChan <- streamResp

				if streamResp.Done {
					close(resChan)
					return
				}
			}
		}
	}()

	// Return the structured response
	return nil
}

// Function to call ollama model
func chatWithModel(prompt string, model string, history []types.HistoryItem) (*ApiResponse, error) {
	// URL of the API endpoint
	url := fmt.Sprintf("http://%s/api/chat", OllamaHost)

	messages := history
	if prompt != "" {
		messages = append(history, types.HistoryItem{
			Role:    "user",
			Content: prompt,
		})
	}

	payload := RequestPayload{
		Model:     model,
		Messages:  messages,
		Stream:    false,
		KeepAlive: KeepAliveTime,
	}

	// Marshal the payload into JSON
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	// Create a new HTTP request
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}

	// Set the appropriate headers
	req.Header.Set("Content-Type", "application/json")

	// Make the HTTP request using the default client
	client := &http.Client{}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	// Read the response body
	responseData, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	log.Printf("Response: %v", string(responseData))

	// Unmarshal JSON data into ApiResponse struct
	var apiResponse ApiResponse
	if err := json.Unmarshal(responseData, &apiResponse); err != nil {
		return nil, err
	}

	// Return the structured response
	return &apiResponse, nil
}

func sourcesFromChunks(chunks []*types.Chunk) []types.Source {
	sources := []types.Source{} 
	for _, chunk := range chunks {
		skip := false
		for _, source := range sources {
			if source.URL == chunk.SourceURL {
				// Avoid duplicate document links
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		sourceObj := types.Source{
			Title: chunk.Name,
			URL:   chunk.SourceURL,
			Type:  chunk.ConnectorType,
		}
		sources = append(sources, sourceObj)
	}
	return sources
}

func Rerank(ctx context.Context, chunks []*types.Chunk, query string) ([]*types.Chunk, error) {
	if len(chunks) == 0 {
		return []*types.Chunk{}, nil
	}

	return rerankBERT(ctx, chunks, query)
}

// type used to pass chunks to BERT rerank models
type Passage struct {
	ID    int                    `json:"id"`
	Text  string                 `json:"text"`
	Meta  map[string]interface{} `json:"meta"`
	Score float32                `json:"score"`
}

type RerankRequest struct {
	Query    string    `json:"query"`
	Passages []Passage `json:"passages"`
}

func rerankBERT(ctx context.Context, chunks []*types.Chunk, query string) ([]*types.Chunk, error) {
	passages := []Passage{}
	for i, chunk := range chunks {
		passages = append(passages, Passage{
			ID:   i,
			Text: chunk.Text,
			Meta: map[string]interface{}{
				"title": chunk.Name,
			},
		})
	}

	rerankRequest := RerankRequest{
		Query:    query,
		Passages: passages,
	}
	// Marshal data into JSON
	jsonData, err := json.Marshal(rerankRequest)
	if err != nil {
		return nil, fmt.Errorf("error marshaling JSON: %v", err)
	}

	output, err := RunRerankModel(ctx, jsonData)
	if err != nil {
		return nil, fmt.Errorf("error running rerank model: %v", err)
	}

	// Unmarshal the output JSON data
	var res RerankResponse
	err = json.Unmarshal(output, &res)
	if err != nil {
		log.Printf("%s", string(output))
		return nil, fmt.Errorf("error unmarshaling JSON: %v", err)
	}

	// Log the IDs returned by the model
	idCount := make(map[int]int)
	for _, item := range res {
		idCount[item.ID]++
		if idCount[item.ID] > 1 {
			log.Printf("Duplicate ID found: %d", item.ID)
			panic("Duplicate ID found")
		}
	}
	log.Printf("Rerank IDs: %v", idCount)

	finalItems := RerankPrune(res)

	// Use a map to ensure unique chunks
	uniqueChunks := make(map[int]*types.Chunk)
	for _, item := range finalItems {
		if _, exists := uniqueChunks[item.ID]; !exists {
			uniqueChunks[item.ID] = chunks[item.ID]
		}
	}

	finalChunks := make([]*types.Chunk, 0, len(uniqueChunks))
	for _, chunk := range uniqueChunks {
		finalChunks = append(finalChunks, chunk)
	}

	return finalChunks, nil
}

// RerankPrune selects the top N chunks from the reranked list
func RerankPrune(items []RerankResponseItem) []RerankResponseItem {
	if len(items) == 0 {
		return nil
	}
	if len(items) <= MaxNumRerankedChunks {
		return items
	}

	subset := []RerankResponseItem{}
	for i := 0; i < len(items); i++ {
		if len(subset) >= MaxNumRerankedChunks || items[i].Score < RerankNoResultScoreCutoff {
			break
		}

		if len(subset) == 0 || subset[len(subset)-1].Score-items[i].Score <= RerankSoloScoreCliff {
			subset = append(subset, items[i])
		} else {
			break
		}
	}

	return subset
}

type RerankResponse []RerankResponseItem

// Define the struct that matches the JSON structure
type Meta struct {
	Title string `json:"title"`
}

type RerankResponseItem struct {
	ID    int     `json:"id"`
	Text  string  `json:"text"`
	Meta  Meta    `json:"meta"`
	Score float64 `json:"score"`
}

func RunRerankModel(ctx context.Context, jsonData []byte) ([]byte, error) {
	// Execute the Python script and pass JSON data to stdin
	distPath, err := util.GetDistPath()
	if err != nil {
		return nil, fmt.Errorf("failed to get dist path: %v", err)
	}
	rerankFilePath := filepath.Join(distPath, rerankDistPath)
	cmd := exec.CommandContext(ctx, rerankFilePath)
	cmd.Stdin = bytes.NewReader(jsonData)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Print(string(output))
		return nil, fmt.Errorf("error executing script: %v", err)
	}
	return output, nil
}

// ParseStringToIntArray takes a specially formatted string and returns an array of integers
func ParseStringToIntArray(input string) ([]int, error) {
	// Trim the square brackets and split the string by " > "
	parts := strings.Split(strings.ReplaceAll(input, "[", ""), "] > ")

	// Prepare a slice to store the integers
	var result []int

	// Iterate over the parts and convert each one to an integer
	for _, part := range parts {
		part = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(part), "> "), "]")
		if part == "" {
			continue
		}
		num, err := strconv.Atoi(part)
		if err != nil {
			return nil, err // Return an error if conversion fails
		}
		result = append(result, num)
	}

	return result, nil
}

// NOTE: rerankLLM is used for RankGPT style rerankers, which are bundled as
// GGUF and ran by ollama. Not currently in use since pairwise rerankers are
// faster and better performing
const rerankModelName = "custom-zephyr"

// Only used for Llama.cpp rerank models such as rerank-zephyr
func rerankLLM(chunks []*types.Chunk, query string) ([]*types.Chunk, error) {
	messages, err := MakeRerankMessages(chunks, query)
	if err != nil {
		return nil, fmt.Errorf("unable to create rerank messages: %s", err)
	}
	log.Print(messages)

	resp, err := chatWithModel("", rerankModelName, messages)
	if err != nil {
		return nil, fmt.Errorf("unable to generate rerank response: %s", err)
	}
	log.Print(resp.Message.Content)

	idxs, err := ParseStringToIntArray(resp.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("unable to parse rerank response: %s", err)
	}
	log.Print(idxs)
	if len(idxs) == 10 || (len(idxs) == 6 && idxs[0] == 6 && idxs[5] == 1) {
		// default hallucination value, don't expect num chunks != 10
		log.Printf("Rerank has hallucinated")
		return chunks, nil
	}

	reranked := []*types.Chunk{}
	for _, idx := range idxs {
		reranked = append(reranked, chunks[idx-1])
	}

	return reranked, nil
}

func MakeRerankMessages(chunks []*types.Chunk, query string) ([]types.HistoryItem, error) {
	// Define the data structure to hold the variables for the template
	data := struct {
		Num   int
		Query string
	}{
		Num:   len(chunks),
		Query: query,
	}

	// Define a multiline string literal as the template
	tmpl := `I will provide you with {{ .Num }} passages, each indicated by number identifier [].	Rank the passages based on their relevance to query: {{.Query}}.`

	// Parse the template string
	t, err := template.New("passages").Parse(tmpl)
	if err != nil {
		return nil, err
	}

	var buffer bytes.Buffer
	// Execute the template with the data and output to stdout
	err = t.Execute(&buffer, data)
	if err != nil {
		return nil, err
	}
	content2 := buffer.String()

	tmpl_suffix := "Search Query: {{ .Num }}. \nRank the {num} passages above based on their relevance to the search query. The passages should be listed in descending order using identifiers. The most relevant passages should be listed first. The output format should be [] > [], e.g., [1] > [2]. Only response the ranking results, do not say any word or explain."

	// Parse the template string
	t, err = template.New("passages").Parse(tmpl_suffix)
	if err != nil {
		return nil, err
	}

	var buffer2 bytes.Buffer
	// Execute the template with the data and output to stdout
	err = t.Execute(&buffer2, data)
	if err != nil {
		return nil, err
	}
	suffix := buffer2.String()

	messages := []types.HistoryItem{
		{
			Role:    "system",
			Content: "You are RankGPT, an intelligent assistant that can rank passages based on their relevancy to the query.",
		},
		{
			Role:    "user",
			Content: content2,
		},
		{
			Role:    "assistant",
			Content: "Okay, please provide the passages.",
		},
	}

	for i, chunk := range chunks {
		messages = append(messages, []types.HistoryItem{
			{
				Role:    "user",
				Content: fmt.Sprintf("\n[%d] %s: %s\n", i+1, chunk.Name, chunk.Text),
			},
			{
				Role:    "assistant",
				Content: fmt.Sprintf("Received passage [%d].", i+1),
			},
		}...)
	}
	messages = append(messages, types.HistoryItem{
		Role:    "user",
		Content: suffix,
	})

	return messages, nil
}

// TODO: function calling?
func MakePrompt(chunks []*types.Chunk, query string) string {
	// Create a builder to efficiently concatenate strings
	var builder strings.Builder

	// Append introduction to guide the model's focus
	builder.WriteString("Answer the following question as concisely as possible: ")
	builder.WriteString(query)

	if len(chunks) == 0 {
		builder.WriteString(`\nNo relevant documents were found to answer the
		question. You may answer the query question on the chat history but you
		should prefer to say you don't know if you're not sure.`)
		return builder.String()
	}

	builder.WriteString(`\n You may only use information from the following
	documents to answer the question. If none of them are relevant say you
	don't know. Answer directly and succintly, keeping a professional tone`)

	// Loop through each data chunk and append it followed by a newline
	for i, chunk := range chunks {
		builder.WriteString(fmt.Sprintf("\n===== Document %d ======\n", i))
		builder.WriteString(fmt.Sprintf("Title: %s\n", chunk.Name))
		builder.WriteString(fmt.Sprintf("Content: %s\n", chunk.Text))
	}

	// Return the final combined prompt
	return builder.String()
}

func WritePromptLog(prompt string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("unable to get user home directory: %w", err)
	}
	path := filepath.Join(home, PromptLogFile)
	// Open the file for writing, creating it if it doesn't exist
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	// Write the prompt to the file
	_, err = file.WriteString("\n===\n" + prompt + "\n")
	return err
}

// Struct to define the request payload
type EmbedRequestPayload struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

// Struct to define the API response format
type EmbedApiResponse struct {
	Embedding []float32 `json:"embedding"`
}

// Function to call ollama model
func EmbedFromModel(prompt string) (*EmbedApiResponse, error) {
	// URL of the API endpoint
	url := fmt.Sprintf("http://%s/api/embeddings", OllamaHost)

	// Create the payload
	payload := EmbedRequestPayload{
		Model:  embeddingsModelName,
		Prompt: prompt,
	}

	// Marshal the payload into JSON
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	// Create a new HTTP request
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}

	// Set the appropriate headers
	req.Header.Set("Content-Type", "application/json")

	maxRetries := 3
	initialBackoff := 2 * time.Second
	var response *http.Response
	var responseData []byte
	for i := 0; i < maxRetries; i++ {
		// Make the HTTP request using the default client
		response, err = httpClient.Do(req)
		if err != nil {
			// Check if the error is a timeout
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				// Wait for a backoff period before retrying
				time.Sleep(initialBackoff * time.Duration(i+1))
				continue
			}
			return nil, err
		}
		defer response.Body.Close()

		// Read the response body
		responseData, err = io.ReadAll(response.Body)
		if err != nil {
			return nil, err
		}
		break
	}

	if response == nil {
		return nil, errors.New("failed to get a response after retries")
	}

	// Unmarshal JSON data into ApiResponse struct
	var apiResponse EmbedApiResponse
	if err := json.Unmarshal(responseData, &apiResponse); err != nil {
		return nil, err
	}

	// Return the structured response
	return &apiResponse, nil
}
