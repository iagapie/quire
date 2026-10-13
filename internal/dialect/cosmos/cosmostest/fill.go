// Package cosmostest fills a Cosmos DB emulator with the data the integration tests
// read.
package cosmostest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
)

// Key is the well-known key of the Cosmos DB emulator.
const Key = "C2y6yDjf5/R+ob0N8A7Cgv30VRDJIWEHLM+4QDU5DE2nQ9nDuVTqobD4b8mGGyPMbIZnqyMsEcaGQy67XIw/Jw=="

// Fill creates the quire database: orders partitioned by /customer (250 items, five
// customers c0…c4) and events-2024 partitioned by /id (3 items).
func Fill(ctx context.Context, endpoint string) error {
	cred, err := azcosmos.NewKeyCredential(Key)
	if err != nil {
		return err
	}
	client, err := azcosmos.NewClientWithKey(endpoint, cred, &azcosmos.ClientOptions{ClientOptions: azcore.ClientOptions{
		InsecureAllowCredentialWithHTTP: strings.HasPrefix(endpoint, "http://"),
		Retry:                           policy.RetryOptions{MaxRetries: 2},
	}})
	if err != nil {
		return err
	}
	if _, err := client.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: "quire"}, nil); err != nil && !conflict(err) {
		return err
	}
	db, err := client.NewDatabase("quire")
	if err != nil {
		return err
	}
	fill := func(name, pk string, n int, item func(i int) map[string]any) error {
		// Test packages fill the emulator at the same time: the items are upserted every
		// time, so each one sees them all.
		_, err := db.CreateContainer(ctx, azcosmos.ContainerProperties{ID: name, PartitionKeyDefinition: azcosmos.PartitionKeyDefinition{Paths: []string{pk}}}, nil)
		if err != nil && !conflict(err) {
			return err
		}
		cc, err := db.NewContainer(name)
		if err != nil {
			return err
		}
		for i := range n {
			it := item(i)
			raw, err := json.Marshal(it)
			if err != nil {
				return err
			}
			key, _ := it[strings.TrimPrefix(pk, "/")].(string)
			if _, err := cc.UpsertItem(ctx, azcosmos.NewPartitionKeyString(key), raw, nil); err != nil {
				return err
			}
		}
		return nil
	}
	err = fill("orders", "/customer", 250, func(i int) map[string]any {
		it := map[string]any{"id": fmt.Sprintf("o%03d", i), "customer": fmt.Sprintf("c%d", i%5), "total": float64(i) + 0.5,
			"tags": []string{"a", "b"}, "address": map[string]any{"city": "Chișinău", "zip": i}}
		if i%2 == 0 {
			it["note"] = nil
		}
		return it
	})
	if err != nil {
		return err
	}
	return fill("events-2024", "/id", 3, func(i int) map[string]any { return map[string]any{"id": fmt.Sprintf("e%d", i), "kind": "click"} })
}

func conflict(err error) bool {
	var re *azcore.ResponseError
	return errors.As(err, &re) && re.StatusCode == http.StatusConflict
}
