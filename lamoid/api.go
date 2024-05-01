package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/posthog/posthog-go"

	"github.com/epochlabs-ai/lamoid/lamoid/connectors"
	"github.com/epochlabs-ai/lamoid/lamoid/store"
	"github.com/epochlabs-ai/lamoid/lamoid/types"
)

var (
	PromptLogFile           = ".lamoid/logs/prompt.log" // Relative to home
	NumConcurrentInferences = 3
)

type API struct {
	Syncer            *Syncer
	Context           context.Context
	Posthog           posthog.Client
	PosthogDistinctID string
}

func (a *API) SetupRouter() *mux.Router {
	r := mux.NewRouter()
	r.HandleFunc("/connectors", a.connectorsList).Methods("GET")
	r.HandleFunc("/connectors/{name}/init", a.connectorInit).Methods("GET")
	r.HandleFunc("/connectors/{name}/auth_setup", a.connectorAuthSetup).Methods("GET")
	r.HandleFunc("/connectors/{name}/callback", a.handleConnectorCallback).Methods("GET")
	r.HandleFunc("/prompt", a.handlePrompt).Methods("POST")
	r.HandleFunc("/health", a.health).Methods("GET")

	r.HandleFunc("/sync/force", a.forceSync).Methods("GET")

	// TODO: the following are only available for development

	r.HandleFunc("/mock", a.mockConnectorState).Methods("GET")

	return r
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	// TODO: check for health of subprocesses
	// TODO: return state of syncs and model downloads, to be used during init
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// only for debug/dev purposes
func (a *API) mockConnectorState(w http.ResponseWriter, r *http.Request) {
	state := &types.ConnectorState{
		Name:         "Google Drive",
		Syncing:      true,
		LastSync:     time.Now(),
		NumDocuments: 15,
		NumChunks:    1005,
	}

	err := store.UpdateConnectorState(context.Background(), store.GetWeaviateClient(), state)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to update state: " + err.Error()))
		return
	}
}

func (a *API) connectorsList(w http.ResponseWriter, r *http.Request) {
	states, err := a.Syncer.GetConnectorStates(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to list connectors: " + err.Error()))
		return
	}

	b, err := json.Marshal(states)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to marshal connectors: " + err.Error()))
		return
	}

	w.Write(b)
}

func (a *API) connectorInit(w http.ResponseWriter, r *http.Request) {
	// Should not error when called accidentally multiple times
	// Can be re-invoked to re-init the connector (i.e. to reset stuck syncing state)
	vars := mux.Vars(r)
	connectorName, ok := vars["name"]
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("No connector name provided"))
		return
	}

	conn, ok := connectors.AllConnectors[connectorName]
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Unknown connector name"))
		return
	}

	err := conn.Init(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to init connector: " + err.Error()))
		return
	}

	err = a.Syncer.AddConnector(conn)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to add connector: " + err.Error()))
		return
	}
}

func (a *API) connectorAuthSetup(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	connectorName, ok := vars["name"]
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("No connector name provided"))
		return
	}

	conn, ok := connectors.AllConnectors[connectorName]
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Unknown connector name"))
		return
	}
	err := conn.AuthSetup(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to perform initial auth with google: " + err.Error()))
		return
	}
}

func (a *API) handleConnectorCallback(w http.ResponseWriter, r *http.Request) {
	queryParts := r.URL.Query()
	// Google returns it as "code"
	code := queryParts.Get("code")
	if code == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("No code in request"))
		return
	}

	errStr := queryParts.Get("error")
	if errStr != "" {
		log.Printf("Error in Google callback: %s\n", errStr)
	}

	vars := mux.Vars(r)
	connectorName, ok := vars["name"]
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("No connector name provided"))
		return
	}

	conn, ok := connectors.AllConnectors[connectorName]
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Unknown connector name"))
		return
	}

	err := conn.AuthCallback(r.Context(), code)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to authenticate with Google: " + err.Error()))
	}
}

func (a *API) forceSync(w http.ResponseWriter, r *http.Request) {
	err := a.Syncer.SyncNow(a.Context)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to sync: " + err.Error()))
	}
}

type PullRequestPayload struct {
	Name   string `json:"name"`
	Stream bool   `json:"stream"`
}

type PullApiResponse struct {
	Status string `json:"status"`
}

// pullModel makes a POST request to the specified URL with the given payload
// and returns nil only if the response status is "success".
func pullModel(name string, stream bool) error {
	url := "http://localhost:11434/api/pull"

	// Create the payload
	payload := PullRequestPayload{
		Name:   name,
		Stream: stream,
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

	// Set the Content-Type header
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

	// Unmarshal JSON data into ApiResponse struct
	var apiResponse PullApiResponse
	if err := json.Unmarshal(responseData, &apiResponse); err != nil {
		return err
	}

	// Check if the status is "success"
	if apiResponse.Status != "success" {
		return fmt.Errorf("API response status is not 'success'")
	}

	return nil
}

func waitForOllama(ctx context.Context) error {
	ollama_url := "http://localhost:11434"

	// Poll the ollama URL every 5 seconds until the context is cancelled
	for {
		resp, err := httpClient.Get(ollama_url)
		log.Print(resp)
		if err == nil {
			log.Printf("Ollama is up and running")
			resp.Body.Close()
			return nil
		}
		select {
		case <-time.After(5 * time.Second):
			log.Printf("Waited 5 sec")
			continue
		case <-ctx.Done():
			return fmt.Errorf("context cancelled during wait: %w", ctx.Err())
		}
	}
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
	url := "http://localhost:11434/api/embeddings"

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

	// Unmarshal JSON data into ApiResponse struct
	var apiResponse EmbedApiResponse
	if err := json.Unmarshal(responseData, &apiResponse); err != nil {
		return nil, err
	}

	// Return the structured response
	return &apiResponse, nil
}

// Struct to define the request payload
type RequestPayload struct {
	Model     string              `json:"model"`
	Messages  []types.HistoryItem `json:"messages"`
	Stream    bool                `json:"stream"`
	KeepAlive string              `json:"keep_alive"`
	Format    string              `json:"format"`
}

// Struct to define the API response format
type ApiResponse struct {
	Model              string            `json:"model"`
	CreatedAt          time.Time         `json:"created_at"`
	Message            types.HistoryItem `json:"message"`
	Done               bool              `json:"done"`
	Context            []int             `json:"context"`
	TotalDuration      int64             `json:"total_duration"`
	LoadDuration       int64             `json:"load_duration"`
	PromptEvalCount    int               `json:"prompt_eval_count"`
	PromptEvalDuration int64             `json:"prompt_eval_duration"`
	EvalCount          int               `json:"eval_count"`
	EvalDuration       int64             `json:"eval_duration"`
}

type PromptRequest struct {
	Prompt  string              `json:"prompt"`
	History []types.HistoryItem `json:"history"`
}

func (a *API) handlePrompt(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	var promptReq PromptRequest
	defer r.Body.Close()
	err := json.NewDecoder(r.Body).Decode(&promptReq)
	if err != nil {
		// return HTTP 400 bad request
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Failed to decode request"))
	}

	// Call Ollama embeddings model to get embeddings for the prompt
	resp, err := EmbedFromModel(promptReq.Prompt)
	if err != nil {
		http.Error(w, "Failed to get embeddings", http.StatusInternalServerError)
		return
	}
	embedTime := time.Now()

	embeddings := resp.Embedding
	log.Printf("Performing vector search")

	// Perform vector similarity search and get list of most relevant results
	searchResults, err := store.HybridSearch(
		r.Context(),
		store.GetWeaviateClient(),
		promptReq.Prompt,
		embeddings,
	)
	if err != nil {
		http.Error(w, "Failed to search for vectors", http.StatusInternalServerError)
		return
	}
	searchTime := time.Now()

	// Rerank the results
	rerankedChunks, err := Rerank(r.Context(), searchResults, promptReq.Prompt)
	if err != nil {
		log.Printf("Failed to rerank search results: %s", err)
		http.Error(w, "Failed to rerank search results", http.StatusInternalServerError)
		return
	}
	rerankTime := time.Now()

	llmPrompt := MakePrompt(rerankedChunks, promptReq.Prompt)
	log.Printf("LLM Prompt: %s", llmPrompt)
	err = WritePromptLog(llmPrompt)
	if err != nil {
		log.Printf("Failed to write prompt to log: %s", err)
		http.Error(w, "Failed to write prompt to log", http.StatusInternalServerError)
		return
	}

	genResp, err := chatWithModel(llmPrompt, promptReq.History)
	if err != nil {
		log.Printf("Failed to generate response: %s", err)
		http.Error(w, "Failed to generate response", http.StatusInternalServerError)
		return

	}
	response := PromptResponse{
		Content:    genResp.Message.Content,
		SourceURLs: urlsFromChunks(rerankedChunks),
	}

	err = WritePromptLog(response.Content)
	if err != nil {
		log.Printf("Failed to write prompt to log: %s", err)
		http.Error(w, "Failed to write prompt to log", http.StatusInternalServerError)
		return
	}
	doneTime := time.Now()

	log.Printf("Response: %v", response)
	b, err := json.Marshal(response)
	if err != nil {
		http.Error(w, "Failed to marshal search results", http.StatusInternalServerError)
		return
	}
	a.Posthog.Enqueue(posthog.Capture{
		DistinctId: a.PosthogDistinctID,
		Event:      "Prompt",
		Properties: posthog.NewProperties().
			Set("total_duration", doneTime.Sub(startTime).String()).
			Set("generation_duration", doneTime.Sub(rerankTime).String()).
			Set("rerank_duration", rerankTime.Sub(searchTime).String()).
			Set("search_duration", searchTime.Sub(embedTime).String()).
			Set("embed_duration", embedTime.Sub(startTime).String()).
			Set("num_search_results", len(searchResults)).
			Set("num_reranked_results", len(rerankedChunks)),
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(b)
}
