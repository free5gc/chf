package testutil

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Mongo opens an isolated test database and drops only that database on cleanup.
func Mongo(t *testing.T) *mongo.Database {
	t.Helper()
	uri := os.Getenv("CHF_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("set CHF_TEST_MONGO_URI to run isolated MongoDB integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	require.NoError(t, err)
	db := client.Database("chf_test_" + uuid.NewString())
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		require.NoError(t, db.Drop(closeCtx))
		require.NoError(t, client.Disconnect(closeCtx))
	})
	require.NoError(t, client.Ping(ctx, nil))
	return db
}
