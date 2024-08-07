package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/verbis-ai/verbis/verbis/keychain"
	"github.com/verbis-ai/verbis/verbis/types"
)

const (
	googleCredentialFile = "credentials.json"

	// Exponential backoff settings
	initialBackoff = 500 * time.Millisecond
	maxBackoff     = 64 * time.Second
	maxRetries     = 10
)

func NewGoogleDriveConnector(creds types.BuildCredentials, st types.Store) types.Connector {
	return &GoogleDriveConnector{
		BaseConnector: BaseConnector{
			connectorType: types.ConnectorTypeGoogleDrive,
			store:         st,
		},
		GoogleJSONCreds: creds.GoogleJSONCreds,
	}
}

type GoogleDriveConnector struct {
	BaseConnector
	GoogleJSONCreds string
}

func (g *GoogleDriveConnector) getClient(ctx context.Context, config *oauth2.Config) (*http.Client, error) {
	// Token from Keychain
	tok, err := keychain.TokenFromKeychain(g.ID(), g.Type())
	if err != nil {
		return nil, err
	}
	return config.Client(ctx, tok), nil
}

func (g *GoogleDriveConnector) requestOauthWeb(config *oauth2.Config) error {
	config.RedirectURL = fmt.Sprintf("http://127.0.0.1:8081/connectors/%s/callback", g.ID())
	log.Printf("Requesting token from web with redirectURL: %v", config.RedirectURL)
	authURL := config.AuthCodeURL(g.ID(), oauth2.AccessTypeOffline)
	fmt.Printf("Your browser has been opened to visit:\n%v\n", authURL)

	// Open URL in the default browser
	return exec.Command("open", authURL).Start()
}

var driveScopes []string = []string{
	drive.DriveMetadataReadonlyScope,
	drive.DriveReadonlyScope,
	"https://www.googleapis.com/auth/userinfo.email",
}

func driveConfigFromJSON(googleJSONCreds string) (*oauth2.Config, error) {
	return google.ConfigFromJSON([]byte(googleJSONCreds), driveScopes...)
}

func (g *GoogleDriveConnector) AuthSetup(ctx context.Context) error {
	config, err := driveConfigFromJSON(g.GoogleJSONCreds)
	if err != nil {
		return fmt.Errorf("unable to get google config: %s", err)
	}
	fmt.Println("Google Drive AuthSetup")
	fmt.Println(config)
	_, err = keychain.TokenFromKeychain(g.ID(), g.Type())
	if err == nil {
		// TODO: check for expiry of refresh token
		log.Print("Token found in keychain.")
		return nil
	}
	log.Print("No token found in keychain. Getting token from web.")
	err = g.requestOauthWeb(config)
	if err != nil {
		log.Printf("Unable to request token from web: %v", err)
	}
	return nil
}

// TODO: handle token expiries
func (g *GoogleDriveConnector) AuthCallback(ctx context.Context, authCode string) error {
	config, err := driveConfigFromJSON(g.GoogleJSONCreds)
	if err != nil {
		return fmt.Errorf("unable to get google config: %s", err)
	}

	config.RedirectURL = fmt.Sprintf("http://127.0.0.1:8081/connectors/%s/callback", g.ID())
	log.Printf("Config: %v", config)
	tok, err := config.Exchange(ctx, authCode)
	if err != nil {
		return fmt.Errorf("unable to retrieve token from web: %v", err)
	}

	err = keychain.SaveTokenToKeychain(tok, g.ID(), g.Type())
	if err != nil {
		return fmt.Errorf("unable to save token to keychain: %v", err)
	}

	client := config.Client(ctx, tok)
	email, err := getUserEmail(client)
	if err != nil {
		return fmt.Errorf("unable to get user email: %v", err)
	}
	log.Printf("User email: %s", email)
	g.user = email

	state, err := g.Status(ctx)
	if err != nil {
		return fmt.Errorf("unable to get connector state: %v", err)
	}

	state.User = g.User()
	return g.UpdateConnectorState(ctx, state)
}

func getUserEmail(client *http.Client) (string, error) {
	resp, err := client.Get("https://www.googleapis.com/oauth2/v2/userinfo?alt=json")
	if err != nil {
		return "", fmt.Errorf("unable to get user info: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to get user info: status %s", resp.Status)
	}

	var userInfo struct {
		Email string `json:"email"`
	}

	err = json.NewDecoder(resp.Body).Decode(&userInfo)
	if err != nil {
		return "", fmt.Errorf("unable to decode user info: %v", err)
	}

	return userInfo.Email, nil
}

func (g *GoogleDriveConnector) Sync(lastSync time.Time, chunkChan chan types.ChunkSyncResult, errChan chan error) {
	defer close(chunkChan)
	if err := g.context.Err(); err != nil {
		errChan <- fmt.Errorf("context error: %s", err)
		return
	}

	config, err := driveConfigFromJSON(g.GoogleJSONCreds)
	if err != nil {
		errChan <- fmt.Errorf("unable to get google config: %s", err)
		return
	}

	client, err := g.getClient(g.context, config)
	if err != nil {
		errChan <- fmt.Errorf("unable to get client: %v", err)
		return
	}

	srv, err := drive.NewService(g.context, option.WithHTTPClient(client))
	if err != nil {
		errChan <- fmt.Errorf("unable to retrieve Drive client: %v", err)
		return
	}

	err = g.listFiles(g.context, srv, lastSync, chunkChan)
	if err != nil {
		errChan <- fmt.Errorf("unable to list files: %v", err)
		return
	}
}

func (g *GoogleDriveConnector) processFile(ctx context.Context, service *drive.Service, file *drive.File, chunkChan chan types.ChunkSyncResult) {
	var content string
	var err error
	if file.MimeType == "application/vnd.google-apps.document" {
		content, err = exportFile(service, file.Id, "text/plain")
	} else if file.MimeType == "application/vnd.google-apps.spreadsheet" {
		content, err = exportFile(service, file.Id, "text/csv")
	} else if file.MimeType == "application/vnd.google-apps.presentation" {
		content, err = exportFile(service, file.Id, "text/plain")
	} else {
		content, err = downloadAndParseBinaryFile(ctx, service, file)
		if err != nil {
			chunkChan <- types.ChunkSyncResult{
				Err: fmt.Errorf("unable to process binary file %s: %v", file.Name, err),
			}
			return
		}
	}
	if err != nil {
		chunkChan <- types.ChunkSyncResult{
			Err: fmt.Errorf("unable to export file %s of mimetype %s: %v", file.Name, file.MimeType, err),
		}
		return
	}

	log.Printf("Document: %s, %s, %s", file.Name, file.CreatedTime, file.ModifiedTime)
	createdAt, err := time.Parse(time.RFC3339, file.CreatedTime)
	if err != nil {
		log.Printf("Error parsing created time %s: %v", file.CreatedTime, err)
		createdAt = time.Now()
	}

	updatedAt, err := time.Parse(time.RFC3339, file.ModifiedTime)
	if err != nil {
		log.Printf("Error parsing modified time %s: %v", file.ModifiedTime, err)
		updatedAt = time.Now()
	}

	document := types.Document{
		UniqueID:      file.Id,
		Name:          file.Name,
		SourceURL:     file.WebViewLink,
		ConnectorID:   g.ID(),
		ConnectorType: string(g.Type()),
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
	}

	// TODO: ideally this should live at the top level but we need to refactor the syncer first
	err = g.store.DeleteDocumentChunks(ctx, document.UniqueID, g.ID())
	if err != nil {
		// Not a fatal error, just log it and leave the old chunks behind
		log.Printf("Unable to delete chunks for document %s: %v", document.UniqueID, err)
	}

	emitChunks(file.Name, content, document, chunkChan)
}

func (g *GoogleDriveConnector) listFiles(ctx context.Context, service *drive.Service, lastSync time.Time, chunkChan chan types.ChunkSyncResult) error {
	pageToken := ""
	retryCount := 0
	maxRetryCount := 3
	retryBackoffSecs := 5

	for {
		q := service.Files.List().
			PageSize(10).
			Fields("nextPageToken, files(id, name, webViewLink, createdTime, modifiedTime, mimeType)").
			OrderBy("modifiedTime desc").Context(ctx)
		if !lastSync.IsZero() {
			q = q.Q("modifiedTime > '" + lastSync.Format(time.RFC3339) + "'")
		}
		if pageToken != "" {
			q = q.PageToken(pageToken)
		}

		r, err := q.Do()
		if err != nil {
			retryCount += 1
			if retryCount < maxRetryCount {
				if ctx.Err() != nil {
					// Tackle cancellation
					return ctx.Err()
				}
				time.Sleep(time.Duration(retryBackoffSecs) * time.Second)
				continue
			}
			return fmt.Errorf("unable to retrieve files: %v", err)
		}
		retryCount = 0 // Reset retry count after a successful operation

		// Max parallelism is number of files per page (10)
		wg := sync.WaitGroup{}
		for _, file := range r.Files {
			wg.Add(1)
			go func(f *drive.File) {
				defer wg.Done()
				g.processFile(ctx, service, f, chunkChan)
			}(file)
		}
		wg.Wait()

		pageToken = r.NextPageToken
		if pageToken == "" {
			break
		}
	}
	return nil
}

func exportFile(service *drive.Service, fileId string, mimeType string) (string, error) {
	var resp *http.Response
	var err error

	for retry := 0; retry < maxRetries; retry++ {
		resp, err = service.Files.Export(fileId, mimeType).Download()
		if err == nil {
			break
		}

		// Check if the error is due to user rate limit exceeded
		if gErr, ok := err.(*googleapi.Error); ok && gErr.Code == http.StatusForbidden && gErr.Message == "User rate limit exceeded" {
			backoff := time.Duration(math.Min(float64(initialBackoff)*math.Pow(2, float64(retry)), float64(maxBackoff)))
			fmt.Printf("Rate limit exceeded. Retrying in %v...\n", backoff)
			time.Sleep(backoff)
		} else {
			return "", err
		}
	}

	if err != nil {
		return "", errors.New("failed to download file after retries")
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func downloadFile(service *drive.Service, fileId string) (string, error) {
	var resp *http.Response
	var err error

	for retry := 0; retry < maxRetries; retry++ {
		resp, err = service.Files.Get(fileId).Download()
		if err == nil {
			break
		}

		if shouldRetry(err) {
			backoff := calculateBackoff(retry)
			fmt.Printf("Error: %v. Retrying in %v...\n", err, backoff)
			time.Sleep(backoff)
		} else {
			return "", fmt.Errorf("failed to download file: %v", err)
		}
	}

	if err != nil {
		return "", fmt.Errorf("failed to download file after retries: %v", err)
	}
	defer resp.Body.Close()

	tempFilePath, err := createTempFilePath(fileId)
	if err != nil {
		return "", err
	}

	outFile, err := os.Create(tempFilePath)
	if err != nil {
		return "", fmt.Errorf("failed to create temporary file: %v", err)
	}
	defer outFile.Close()

	if _, err = io.Copy(outFile, resp.Body); err != nil {
		return "", fmt.Errorf("failed to write file to disk: %v", err)
	}

	return tempFilePath, nil
}

func shouldRetry(err error) bool {
	if gErr, ok := err.(*googleapi.Error); ok && gErr.Code >= 500 {
		return true
	}
	if netErr, ok := err.(net.Error); ok && netErr.Temporary() {
		return true
	}
	return false
}

func calculateBackoff(retry int) time.Duration {
	return time.Duration(math.Min(float64(initialBackoff)*math.Pow(2, float64(retry)), float64(maxBackoff)))
}

func createTempFilePath(fileId string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user home directory: %v", err)
	}

	tempDir := filepath.Join(homeDir, ".verbis", "tmp")
	if err = os.MkdirAll(tempDir, os.ModePerm); err != nil {
		return "", fmt.Errorf("failed to create temporary directory: %v", err)
	}

	return filepath.Join(tempDir, fileId), nil
}

func downloadAndParseBinaryFile(ctx context.Context, service *drive.Service, file *drive.File) (string, error) {
	_, ok := SupportedMimeTypes[file.MimeType]
	if !ok {
		log.Printf("Unsupported MIME type: %s", file.MimeType)
		return "", nil
	}
	log.Printf("Processing binary file: %s", file.Name)

	tempFilePath, err := downloadFile(service, file.Id)
	if err != nil {
		return "", fmt.Errorf("failed to download file: %v", err)
	}
	log.Printf("Finished downloading binary file: %s", file.Name)

	request := &ParseRequest{
		Type: file.MimeType,
		Path: tempFilePath,
	}
	content, err1 := ParseBinaryFile(ctx, request)
	err2 := os.Remove(tempFilePath) // Delete the file after processing
	log.Printf("Finished parsing binary file %s", file.Name)

	if err1 != nil {
		return "", fmt.Errorf("failed to parse binary file: %s", err1)
	}
	if err2 != nil {
		log.Printf("Error deleting file %s: %s", tempFilePath, err2)
	}

	return content, nil
}
