package rabbitmq_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/cozy/cozy-stack/model/instance/lifecycle"
	"github.com/cozy/cozy-stack/pkg/config/config"
	"github.com/cozy/cozy-stack/pkg/rabbitmq"
	"github.com/cozy/cozy-stack/tests/testutils"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"
)

// TestUserCreatedHandlerStoresMatrixID checks that the Matrix ID a user.created
// message carries lands in the instance settings document, which is where
// buildRequest reads it from.
func TestUserCreatedHandlerStoresMatrixID(t *testing.T) {
	config.UseTestFile(t)
	testutils.NeedCouchdb(t)

	// A forced OIDC context lets a message through without a passphrase hash,
	// which is not what this test is about.
	contextName := "matrix-id-test"
	conf := config.GetConfig()
	conf.Authentication = map[string]interface{}{
		contextName: map[string]interface{}{"disable_password_authentication": true},
	}

	newInstance := func(t *testing.T) string {
		t.Helper()
		domain := fmt.Sprintf("matrix-id-%d.example", time.Now().UnixNano())
		inst, err := lifecycle.Create(&lifecycle.Options{
			Domain:      domain,
			Email:       "alice@example.org",
			ContextName: contextName,
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = lifecycle.Destroy(domain) })
		return inst.Domain
	}

	storedMatrixID := func(t *testing.T, domain string) string {
		t.Helper()
		inst, err := lifecycle.GetInstance(domain)
		require.NoError(t, err)
		settings, err := inst.SettingsDocument()
		require.NoError(t, err)
		id, _ := settings.M["matrix_id"].(string)
		return id
	}

	handle := func(t *testing.T, domain, matrixID string) error {
		t.Helper()
		body, err := json.Marshal(rabbitmq.UserCreatedMessage{
			TwakeID:       "alice",
			WorkplaceFqdn: domain,
			MatrixID:      matrixID,
		})
		require.NoError(t, err)

		return rabbitmq.NewUserCreatedHandler().
			Handle(context.Background(), amqp.Delivery{Body: body})
	}

	t.Run("stores the matrix id it receives", func(t *testing.T) {
		domain := newInstance(t)

		require.NoError(t, handle(t, domain, "@al.ice:example.org"))
		require.Equal(t, "@al.ice:example.org", storedMatrixID(t, domain))
	})

	t.Run("a redelivery leaves the same value in place", func(t *testing.T) {
		domain := newInstance(t)

		require.NoError(t, handle(t, domain, "@al.ice:example.org"))
		require.NoError(t, handle(t, domain, "@al.ice:example.org"))
		require.Equal(t, "@al.ice:example.org", storedMatrixID(t, domain))
	})

	t.Run("a malformed matrix id is dropped, not stored", func(t *testing.T) {
		domain := newInstance(t)

		require.NoError(t, handle(t, domain, "al.ice@example.org"))
		require.Empty(t, storedMatrixID(t, domain))
	})

	t.Run("a message without a matrix id stores nothing", func(t *testing.T) {
		domain := newInstance(t)

		require.NoError(t, handle(t, domain, ""))
		require.Empty(t, storedMatrixID(t, domain))
	})
}
