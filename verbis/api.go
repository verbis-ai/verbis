package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/posthog/posthog-go"

	"github.com/verbis-ai/verbis/verbis/connectors"
	"github.com/verbis-ai/verbis/verbis/store"
	"github.com/verbis-ai/verbis/verbis/types"
)

var (
	PromptLogFile = ".verbis/logs/prompt.log" // Relative to home
)

type API struct {
	Syncer            *Syncer
	Context           *BootContext
	Posthog           posthog.Client
	PosthogDistinctID string
}

func (a *API) SetupRouter() *mux.Router {
	r := mux.NewRouter()
	r.HandleFunc("/connectors", a.connectorsList).Methods("GET")
	r.HandleFunc("/connectors/{type}/init", a.connectorInit).Methods("GET")
	r.HandleFunc("/connectors/{type}/request", a.connectorRequest).Methods("GET")
	// TODO: auth_setup and callback are theoretically per connector and not per
	// connector type. The ID of the connector should be inferred and passed as
	// a state variable in the oauth flow.
	r.HandleFunc("/connectors/{connector_id}/auth_setup", a.connectorAuthSetup).Methods("GET")
	r.HandleFunc("/connectors/{connector_id}/callback", a.handleConnectorCallback).Methods("GET")
	r.HandleFunc("/connectors/auth_complete", a.authComplete).Methods("GET")

	r.HandleFunc("/conversations", a.listConversations).Methods("GET")
	r.HandleFunc("/conversations", a.createConversation).Methods("POST")
	r.HandleFunc("/conversations/{conversation_id}/prompt", a.handlePrompt).Methods("POST")

	r.HandleFunc("/health", a.health).Methods("GET")
	r.HandleFunc("/sync/force", a.forceSync).Methods("GET")

	return r
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	// TODO: check for health of subprocesses
	// TODO: return state of syncs and model downloads, to be used during init
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(fmt.Sprintf("{\"boot_state\": \"%s\"}", a.Context.State)))
}

func (a *API) connectorRequest(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	connectorType, ok := vars["type"]
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("No connector name provided"))
		return
	}

	err := a.Posthog.Enqueue(posthog.Capture{
		DistinctId: a.PosthogDistinctID,
		Event:      "ConnectorRequest",
		Properties: posthog.NewProperties().
			Set("connector_type", connectorType),
	})
	if err != nil {
		log.Printf("Failed to enqueue connector request: %s\n", err)
		http.Error(w, "Failed to enqueue connector request", http.StatusInternalServerError)
		return
	}
}

func (a *API) listConversations(w http.ResponseWriter, r *http.Request) {
	conversations, err := store.ListConversations(r.Context(), store.GetWeaviateClient())
	if err != nil {
		log.Printf("Failed to list conversations: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to list conversations: " + err.Error()))
		return
	}

	b, err := json.Marshal(conversations)
	if err != nil {
		log.Printf("Failed to marshal conversations: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to marshal conversations: " + err.Error()))
		return
	}

	w.Write(b)
}

func (a *API) createConversation(w http.ResponseWriter, r *http.Request) {
	conversationID, err := store.CreateConversation(r.Context(), store.GetWeaviateClient())
	if err != nil {
		log.Printf("Failed to create conversation: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to create conversation: " + err.Error()))
		return
	}

	b, err := json.Marshal(map[string]string{"id": conversationID})
	if err != nil {
		log.Printf("Failed to marshal conversation: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to marshal conversation: " + err.Error()))
		return
	}

	w.Write(b)
}

func (a *API) connectorsList(w http.ResponseWriter, r *http.Request) {
	states, err := a.Syncer.GetConnectorStates(r.Context())
	if err != nil {
		log.Printf("Failed to list connectors: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to list connectors: " + err.Error()))
		return
	}

	b, err := json.Marshal(states)
	if err != nil {
		log.Printf("Failed to marshal connectors: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to marshal connectors: " + err.Error()))
		return
	}

	w.Write(b)
}

func (a *API) authComplete(w http.ResponseWriter, r *http.Request) {
	// TODO: render page telling the user to go back to the desktop app
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Auth complete"))
}

func (a *API) connectorInit(w http.ResponseWriter, r *http.Request) {
	// Should not error when called accidentally multiple times
	// Can be re-invoked to re-init the connector (i.e. to reset stuck syncing state)
	vars := mux.Vars(r)
	connectorType, ok := vars["type"]
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("No connector name provided"))
		return
	}

	constructor, ok := connectors.AllConnectors[connectorType]
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Unknown connector name"))
		return
	}

	// Create a new connector object and initialize it
	// The Init method is responsible for picking up existing configuration from
	// the store, and discovering credentials
	conn := constructor()

	log.Printf("Initializing connector type: %s id: %s", conn.Type(), conn.ID())
	err := conn.Init(r.Context(), "")
	if err != nil {
		log.Printf("Failed to init connector: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to init connector: " + err.Error()))
		return
	}
	log.Printf("Connector %s %s initialized", conn.Type(), conn.ID())

	// Add the connector to the syncer so that it may start syncing
	err = a.Syncer.AddConnector(conn)
	if err != nil {
		log.Printf("Failed to add connector: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to add connector: " + err.Error()))
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(fmt.Sprintf(`{"id": "%s"}`, conn.ID())))
}

func (a *API) connectorAuthSetup(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	connectorID, ok := vars["connector_id"]
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("No connector ID provided"))
		return
	}

	conn := a.Syncer.GetConnector(connectorID)
	if conn == nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Unknown connector ID"))
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
	connectorID, ok := vars["connector_id"]
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("No connector name provided"))
		return
	}

	stateParam := queryParts.Get("state")
	// If any state is provided it must match the connector ID
	if stateParam != "" && stateParam != connectorID {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("State does not match connector ID"))
		return
	}

	conn := a.Syncer.GetConnector(connectorID)
	if conn == nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Unknown connector ID"))
		return
	}
	err := conn.AuthCallback(r.Context(), code)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to authenticate with Google: " + err.Error()))
		return
	}

	state, err := conn.Status(a.Context)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to get connector state: " + err.Error()))
		return
	}
	state.AuthValid = true // TODO: delegate this logic to the connector implementation
	err = conn.UpdateConnectorState(a.Context, state)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Failed to update connector state: " + err.Error()))
		return
	}

	// Trigger a background sync, it should silently quit if a sync is already
	// running for this connector
	a.Syncer.ASyncNow(a.Context)

	// TODO: Render a done page
	w.Write([]byte("Google authentication is complete, you may close this tab and return to the Verbis desktop app"))
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
	url := fmt.Sprintf("http://%s/api/pull", OllamaHost)

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
	Prompt string `json:"prompt"`
}

type StreamResponseHeader struct {
	Sources []map[string]string `json:"sources"` // Only returned on the first response
}

func (a *API) handlePrompt(w http.ResponseWriter, r *http.Request) {
	log.Printf("Start of handlePrompt")
	startTime := time.Now()

	vars := mux.Vars(r)
	conversationID, ok := vars["conversation_id"]
	if !ok {
		http.Error(w, "No conversation ID provided", http.StatusBadRequest)
		return
	}

	var promptReq PromptRequest
	defer r.Body.Close()
	err := json.NewDecoder(r.Body).Decode(&promptReq)
	if err != nil {
		// return HTTP 400 bad request
		http.Error(w, "Failed to decode request", http.StatusBadRequest)
	}

	conversation, err := store.GetConversation(r.Context(), store.GetWeaviateClient(), conversationID)
	if err != nil {
		log.Printf("Failed to get conversation: %s", err)
		http.Error(w, "Failed to get conversation: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	// Call Ollama embeddings model to get embeddings for the prompt
	resp, err := EmbedFromModel(promptReq.Prompt)
	if err != nil {
		log.Printf("Failed to get embeddings: %s", err)
		http.Error(w, "Failed to get embeddings "+err.Error(), http.StatusInternalServerError)
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

	// Add all previous conversation chunks for reranking
	for _, chunkHash := range conversation.ChunkHashes {
		chunk, err := store.GetChunkByHash(r.Context(), store.GetWeaviateClient(), chunkHash)
		if err != nil {
			log.Printf("Failed to get chunk by hash: %s", err)
			http.Error(w, "Failed to get chunk by hash", http.StatusInternalServerError)
			return
		}
		searchResults = append(searchResults, chunk)
	}

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

	streamChan := make(chan StreamResponse)
	err = chatWithModelStream(r.Context(), llmPrompt, generationModelName, conversation.History, streamChan)
	if err != nil {
		log.Printf("Failed to generate response: %s", err)
		http.Error(w, "Failed to generate response", http.StatusInternalServerError)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		// TODO: if we run into this, fall back to non-streaming
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// First write the header response
	err = json.NewEncoder(w).Encode(StreamResponseHeader{
		Sources: sourcesFromChunks(rerankedChunks),
	})
	if err != nil {
		http.Error(w, "Failed to write response", http.StatusInternalServerError)
		return
	}

	// Write a newline after the header
	_, err = w.Write([]byte("\n"))
	if err != nil {
		http.Error(w, "Failed to write newline", http.StatusInternalServerError)
		return
	}

	timeToFirstToken := time.Time{}
	responseAcc := ""
	streamCount := 0
	for item := range streamChan {
		if timeToFirstToken.IsZero() {
			timeToFirstToken = time.Now()
		}
		streamCount++
		responseAcc += item.Message.Content
		json.NewEncoder(w).Encode(item)
		_, err = w.Write([]byte("\n"))
		if err != nil {
			http.Error(w, "Failed to write newline", http.StatusInternalServerError)
			return
		}
		flusher.Flush()
	}

	err = WritePromptLog(responseAcc)
	if err != nil {
		log.Printf("Failed to write prompt to log: %s", err)
		http.Error(w, "Failed to write prompt to log", http.StatusInternalServerError)
		return
	}
	doneTime := time.Now()

	conversation.History = append(conversation.History, types.HistoryItem{
		Role:    "assistant",
		Content: responseAcc,
	})

	// Find out which chunks are not already part of the conversation history
	newChunks := []*types.Chunk{}
	for _, chunk := range rerankedChunks {
		found := false
		for _, chunkHash := range conversation.ChunkHashes {
			if chunkHash == chunk.Hash {
				found = true
				break
			}
		}

		if !found {
			newChunks = append(newChunks, chunk)
		}
	}

	err = store.ConversationAppend(r.Context(), store.GetWeaviateClient(), conversationID, []types.HistoryItem{
		{
			Role:    "user",
			Content: promptReq.Prompt,
		},
		{
			Role:    "assistant",
			Content: responseAcc,
		},
	}, newChunks)
	if err != nil {
		log.Printf("Failed to append to conversation: %s", err)
		http.Error(w, "Failed to append to conversation", http.StatusInternalServerError)
		return
	}

	err = a.Posthog.Enqueue(posthog.Capture{
		DistinctId: a.PosthogDistinctID,
		Event:      "Prompt",
		Properties: posthog.NewProperties().
			Set("total_duration", doneTime.Sub(startTime).String()).
			Set("1.search_duration", searchTime.Sub(embedTime).String()).
			Set("2.embed_duration", embedTime.Sub(startTime).String()).
			Set("3.rerank_duration", rerankTime.Sub(searchTime).String()).
			Set("4.gen_ttft_duration", timeToFirstToken.Sub(rerankTime).String()).
			Set("5.gen_stream_duration", doneTime.Sub(timeToFirstToken).String()).
			Set("ttft_duration", timeToFirstToken.Sub(startTime).String()).
			Set("gen_sum_duration", doneTime.Sub(rerankTime).String()).
			Set("num_search_results", len(searchResults)).
			Set("num_reranked_results", len(rerankedChunks)).
			Set("num_streamed_events", streamCount),
	})
	if err != nil {
		log.Printf("Failed to enqueue event: %s\n", err)
		http.Error(w, "Failed to enqueue event", http.StatusInternalServerError)
		return
	}
	log.Printf("End of handlePrompt")
}
