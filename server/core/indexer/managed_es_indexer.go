package indexer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino-ext/components/indexer/es8"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/indexer"
	"github.com/cloudwego/eino/schema"
)

// managedESIndexer sends bounded synchronous batches only after embedding and
// target verification. The locked SDK starts bulk workers before embedding and
// does not close them on its early error paths; managed rejection must not leak
// those workers or flush queued writes after the rejection.
type managedESIndexer struct {
	conf  *es8.IndexerConfig
	guard func(context.Context) error
}

type managedESDocument struct {
	id      string
	fields  map[string]any
	vectors map[string]int
}

func (i *managedESIndexer) GetType() string { return "ManagedElasticsearch" }

func (i *managedESIndexer) IsCallbacksEnabled() bool { return true }

func (i *managedESIndexer) Store(ctx context.Context, docs []*schema.Document, opts ...indexer.Option) (ids []string, err error) {
	ctx = callbacks.EnsureRunInfo(ctx, i.GetType(), components.ComponentOfIndexer)
	ctx = callbacks.OnStart(ctx, &indexer.CallbackInput{Docs: docs})
	defer func() {
		if err != nil {
			callbacks.OnError(ctx, err)
		}
	}()
	options := indexer.GetCommonOptions(&indexer.Options{}, opts...)
	if options.Embedding != nil || len(options.SubIndexes) != 0 {
		return nil, fmt.Errorf("managed indexer does not allow embedding or target overrides")
	}
	if err = i.guard(ctx); err != nil {
		return nil, err
	}
	limit := i.conf.BatchSize
	if limit <= 0 {
		return nil, fmt.Errorf("invalid managed batch size")
	}
	var pending []managedESDocument
	var texts []string
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := i.guard(ctx); err != nil {
			return err
		}
		var vectors [][]float64
		if len(texts) > 0 {
			if i.conf.Embedding == nil {
				return fmt.Errorf("embedding is required")
			}
			info := &callbacks.RunInfo{Component: components.ComponentOfEmbedding}
			if typ, ok := components.GetType(i.conf.Embedding); ok {
				info.Type = typ
			}
			info.Name = info.Type + string(info.Component)
			var embedErr error
			vectors, embedErr = i.conf.Embedding.EmbedStrings(callbacks.ReuseHandlers(ctx, info), texts)
			if embedErr != nil {
				return fmt.Errorf("managed embedding: %w", embedErr)
			}
			if len(vectors) != len(texts) {
				return fmt.Errorf("invalid embedding response count")
			}
		}
		var body bytes.Buffer
		enc := json.NewEncoder(&body)
		for _, doc := range pending {
			for key, pos := range doc.vectors {
				doc.fields[key] = vectors[pos]
			}
			if err := enc.Encode(map[string]any{"index": map[string]any{"_index": i.conf.Index, "_id": doc.id}}); err != nil {
				return err
			}
			if err := enc.Encode(doc.fields); err != nil {
				return err
			}
		}
		// Also guards documents with no embedding fields. No background bulk queue.
		if err := i.guard(ctx); err != nil {
			return err
		}
		res, err := i.conf.Client.Bulk(&body, i.conf.Client.Bulk.WithIndex(i.conf.Index), i.conf.Client.Bulk.WithContext(ctx))
		if err != nil {
			return fmt.Errorf("managed bulk: %w", err)
		}
		defer res.Body.Close()
		if res.IsError() {
			return fmt.Errorf("managed bulk HTTP status %d", res.StatusCode)
		}
		var reply struct {
			Errors bool `json:"errors"`
			Items  []map[string]struct {
				Status int    `json:"status"`
				ID     string `json:"_id"`
				Index  string `json:"_index"`
			} `json:"items"`
		}
		if err := json.NewDecoder(res.Body).Decode(&reply); err != nil {
			return fmt.Errorf("decode managed bulk: %w", err)
		}
		if reply.Errors || len(reply.Items) != len(pending) {
			return fmt.Errorf("managed bulk did not acknowledge every document")
		}
		for n, item := range reply.Items {
			ack, ok := item["index"]
			if len(item) != 1 || !ok || ack.Status < 200 || ack.Status >= 300 || ack.ID != pending[n].id || ack.Index != i.conf.Index {
				return fmt.Errorf("managed bulk unexpected acknowledgement for document %q", pending[n].id)
			}
		}
		for _, doc := range pending {
			ids = append(ids, doc.id)
		}
		pending = nil
		texts = nil
		return nil
	}
	for _, doc := range docs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if doc == nil {
			return nil, fmt.Errorf("nil document")
		}
		fields, mapErr := i.conf.DocumentToFields(ctx, doc)
		if mapErr != nil {
			return nil, fmt.Errorf("managed field mapping: %w", mapErr)
		}
		if doc.ID == "" {
			return nil, fmt.Errorf("document ID is required")
		}
		embedCount := 0
		for _, value := range fields {
			if value.EmbedKey != "" {
				embedCount++
			}
		}
		if embedCount > limit {
			return nil, fmt.Errorf("document embedding fields exceed batch limit")
		}
		if len(pending) >= limit || len(texts)+embedCount > limit {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		mapped := managedESDocument{id: doc.ID, fields: make(map[string]any), vectors: make(map[string]int)}
		for key, value := range fields {
			mapped.fields[key] = value.Value
			if value.EmbedKey == "" {
				continue
			}
			if _, exists := fields[value.EmbedKey]; exists {
				return nil, fmt.Errorf("embedding destination already exists")
			}
			if _, exists := mapped.vectors[value.EmbedKey]; exists {
				return nil, fmt.Errorf("duplicate embedding destination")
			}
			var text string
			if value.Stringify != nil {
				var stringifyErr error
				text, stringifyErr = value.Stringify(value.Value)
				if stringifyErr != nil {
					return nil, stringifyErr
				}
			} else {
				var ok bool
				text, ok = value.Value.(string)
				if !ok {
					return nil, fmt.Errorf("embedding field must be a string")
				}
			}
			mapped.vectors[value.EmbedKey] = len(texts)
			texts = append(texts, text)
		}
		pending = append(pending, mapped)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	callbacks.OnEnd(ctx, &indexer.CallbackOutput{IDs: ids})
	return ids, nil
}
