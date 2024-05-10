package main

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/epochlabs-ai/lamoid/lamoid/connectors"
	"github.com/epochlabs-ai/lamoid/lamoid/store"
	"github.com/epochlabs-ai/lamoid/lamoid/types"
)

const (
	MinChunkSize = 10
)

type Syncer struct {
	connectors      map[string]types.Connector
	syncCheckPeriod time.Duration
	staleThreshold  time.Duration
}

func NewSyncer() *Syncer {
	return &Syncer{
		connectors:      map[string]types.Connector{},
		syncCheckPeriod: 1 * time.Minute,
		staleThreshold:  1 * time.Minute,
	}
}

func (s *Syncer) Init(ctx context.Context) error {
	if len(s.connectors) > 0 {
		// Connectors already initialized, nothing to do
		log.Printf("Syncer init called with %d connectors, skipping state restoration", len(s.connectors))
		return nil
	}

	states, err := store.AllConnectorStates(ctx, store.GetWeaviateClient())
	if err != nil {
		return fmt.Errorf("failed to get connector states: %s", err)
	}
	for _, state := range states {
		c, ok := connectors.AllConnectors[state.ConnectorType]
		if !ok {
			return fmt.Errorf("unknown connector type %s", state.ConnectorType)
		}
		err = c.Init(ctx)
		if err != nil {
			return fmt.Errorf("failed to init connector %s: %s", state.ConnectorID, err)
		}
		err = s.AddConnector(c)
		if err != nil {
			return fmt.Errorf("failed to add connector %s: %s", state.ConnectorID, err)
		}
	}
	return nil
}

func (s *Syncer) AddConnector(c types.Connector) error {
	_, ok := s.connectors[c.ID()]
	if !ok {
		s.connectors[c.ID()] = c
	}
	return nil
}

func (s *Syncer) GetConnector(id string) types.Connector {
	return s.connectors[id]
}

func (s *Syncer) GetConnectorStates(ctx context.Context) ([]*types.ConnectorState, error) {
	states := []*types.ConnectorState{}
	for _, c := range s.connectors {
		state, err := c.Status(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get state for %s: %s", c.ID(), err)
		}
		states = append(states, state)
	}
	return states, nil
}

// On launch, and after every sync_period, find all connectors that are not
// actively syncing
func (s *Syncer) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(s.syncCheckPeriod):
			err := s.SyncNow(ctx)
			if err != nil {
				log.Printf("Failed to sync: %s\n", err)
			}
		}
	}
}

func cleanWhitespace(text string) string {
	// The UTF-8 BOM is sometimes present in text files, and should be removed
	bom := []byte{0xEF, 0xBB, 0xBF}
	text = strings.TrimPrefix(text, string(bom))

	// Replace internal sequences of whitespace with a single space
	spacePattern := regexp.MustCompile(`\s+`)
	text = spacePattern.ReplaceAllString(text, " ")
	// Trim leading and trailing whitespace
	// If the initial text was all whitespace, it should return an empty string
	return strings.TrimSpace(text)
}

func chunkAdder(ctx context.Context, c types.Connector, chunkChan chan types.Chunk, doneChan chan struct{}) {
	// TODO: hold buffer and add vectors in batches
	for chunk := range chunkChan {
		log.Printf("Received chunk of length %d\n", len(chunk.Text))
		saneChunk := cleanWhitespace(chunk.Text)
		log.Printf("Sanitized chunk to length %d\n", len(saneChunk))
		if len(saneChunk) < MinChunkSize {
			log.Printf("Skipping short chunk: %s\n", saneChunk)
			continue
		}
		resp, err := EmbedFromModel(saneChunk)
		if err != nil {
			log.Printf("Failed to get embeddings: %s\n", err)
			continue
		}
		log.Printf("Received embeddings for chunk of length %d", len(saneChunk))
		embedding := resp.Embedding
		chunk.Text = saneChunk
		err = store.AddVectors(ctx, store.GetWeaviateClient(), []types.AddVectorItem{
			{
				Chunk:  chunk,
				Vector: embedding,
			},
		})
		if err != nil {
			log.Printf("Failed to add vectors: %s\n", err)
		}
		log.Printf("Added vectors")

		state, err := c.Status(ctx)
		if err != nil {
			log.Printf("Failed to get status: %s\n", err)
			continue
		}
		state.NumChunks++
		state.NumDocuments++
		err = c.UpdateConnectorState(ctx, state)
		if err != nil {
			log.Printf("Failed to update status: %s\n", err)
			continue
		}
		log.Printf("Added vectors for chunk: %s\n", chunk.SourceURL)
	}
	log.Printf("Chunk channel closed")
	close(doneChan)
}

func (s *Syncer) SyncNow(ctx context.Context) error {
	for _, c := range s.connectors {
		log.Printf("Checking status for connector %s\n", c.ID())
		state, err := c.Status(ctx)
		if err != nil {
			return fmt.Errorf("failed to get status for %s: %s", c.ID(), err)
		}
		if state == nil {
			return fmt.Errorf("nil state for %s", c.ID())
		}

		if !state.AuthValid {
			log.Printf("Auth required for %s\n", c.ID())
			continue
		}

		if state.Syncing {
			log.Printf("Sync already in progress for %s, skipping\n", c.ID())
			continue
		}

		if time.Since(state.LastSync) > s.staleThreshold {
			log.Printf("Sync required for %s\n", c.ID())

			// TODO: replace with atomic get and set for syncing state
			err = store.LockConnector(ctx, store.GetWeaviateClient(), c.ID())
			if err != nil {
				return fmt.Errorf("failed to lock connector %s: %s", c.ID(), err)
			}
			// TODO: unlock at end of for loop, not function return
			defer store.UnlockConnector(ctx, store.GetWeaviateClient(), c.ID())

			newSyncTime := time.Now()

			chunkChan := make(chan types.Chunk) // closed by Sync
			errChan := make(chan error)         // closed by Sync
			doneChan := make(chan struct{})     // closed by chunkAdder

			// TODO: rewrite as sync waitgroup
			go chunkAdder(ctx, c, chunkChan, doneChan)
			go c.Sync(ctx, state.LastSync, chunkChan, errChan)

			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled")
			case <-doneChan:
				log.Printf("Sync complete for %s\n", c.ID())
			case err := <-errChan:
				if err != nil {
					log.Printf("Error during sync for %s: %s\n", c.ID(), err)
				}
				log.Printf("ErrChan closed before DoneChan")
			}
			log.Printf("Sync for connector %s complete\n", c.ID())

			state, err := c.Status(ctx)
			if err != nil {
				return fmt.Errorf("failed to get status for %s: %s", c.ID(), err)
			}

			log.Printf("NumChunks: %d, NumDocuments: %d\n", state.NumChunks, state.NumDocuments)

			state.LastSync = newSyncTime
			state.Syncing = false
			err = c.UpdateConnectorState(ctx, state)
			if err != nil {
				return fmt.Errorf("unable to update last sync for %s: %s", c.ID(), err)
			}
		}
	}

	return nil
}
