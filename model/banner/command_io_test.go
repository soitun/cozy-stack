package banner

import (
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cozy/cozy-stack/model/instance/lifecycle"
	"github.com/cozy/cozy-stack/pkg/config/config"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/prefixer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type commandRoundTripper func(*http.Request) (*http.Response, error)

func (f commandRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCommandPartialFanoutRetriesStorageFailure(t *testing.T) {
	config.UseTestFile(t)
	needCouchDB(t)
	useCommandContexts(t)
	first := newInstance(t, commandContext, "en", "")
	org := "org-" + first.Domain
	first.OrgID = org
	// The instance helper registers cleanup; creation of the other members
	// uses the same org without assuming CouchDB's member ordering.
	require.NoError(t, couchdb.UpdateDoc(prefixer.GlobalPrefixer, first))
	newInstance(t, commandContext, "fr", org)
	newInstance(t, commandContext, "en", org)
	members, err := lifecycle.ListOrgInstancesByID(org)
	require.NoError(t, err)
	require.Len(t, members, 3)
	cmd := fixture(t, "organization")
	cmd.Tenant = org
	failPath := "/" + couchdb.EscapeCouchdbName(members[1].DBPrefix()+"/"+consts.Banners) + "/banner-billing"
	client := config.CouchClient()
	original := client.Transport
	t.Cleanup(func() { client.Transport = original })
	var failed atomic.Bool
	client.Transport = commandRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet && r.URL.Path == failPath && failed.CompareAndSwap(false, true) {
			return nil, errors.New("simulated projection outage")
		}
		return original.RoundTrip(r)
	})
	err = ApplyCommand(cmd)
	require.ErrorContains(t, err, "simulated projection outage")
	assert.NotErrorIs(t, err, ErrInvalidCommand)
	before := storedBanner(t, members[0])
	require.NotNil(t, before)
	assert.Nil(t, storedBanner(t, members[1]))
	assert.Nil(t, storedBanner(t, members[2]))
	retained, err := Stored(members[1], CategoryBilling)
	require.NoError(t, err)
	require.Nil(t, retained, "nothing is recorded for a member whose banner was not written")
	require.NoError(t, ApplyCommand(cmd))
	for _, inst := range members {
		require.NotNil(t, storedBanner(t, inst))
	}
	assert.Equal(t, before.DocRev, storedBanner(t, members[0]).DocRev)
}

func TestCommandClearRetriesProjectionFailure(t *testing.T) {
	config.UseTestFile(t)
	needCouchDB(t)
	useCommandContexts(t)
	inst := newInstance(t, commandContext, "en", "")
	old := materialize(t, inst, 1)
	require.NoError(t, ApplyCommand(old))
	client := config.CouchClient()
	original := client.Transport
	t.Cleanup(func() { client.Transport = original })
	var failed atomic.Bool
	client.Transport = commandRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/banner-billing") && failed.CompareAndSwap(false, true) {
			return nil, errors.New("simulated clear outage")
		}
		return original.RoundTrip(r)
	})
	clear := clearCommand(t, inst, 2)
	require.ErrorContains(t, ApplyCommand(clear), "simulated clear outage")
	retained, err := Stored(inst, CategoryBilling)
	require.NoError(t, err)
	require.False(t, retained.Cleared, "a failed clear must leave the previous decision intact")
	require.Equal(t, int64(1), retained.Revision)
	require.NoError(t, ApplyCommand(old))
	require.NoError(t, ApplyCommand(clear))
	assert.Nil(t, storedBanner(t, inst))
}
