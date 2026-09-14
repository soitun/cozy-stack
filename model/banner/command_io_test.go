package banner_test

import (
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cozy/cozy-stack/model/banner"
	"github.com/cozy/cozy-stack/model/instance/lifecycle"
	"github.com/cozy/cozy-stack/pkg/config/config"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/prefixer"
	"github.com/cozy/cozy-stack/tests/testutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type commandRoundTripper func(*http.Request) (*http.Response, error)

func (f commandRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCommandFanoutContinuesAfterStorageFailures(t *testing.T) {
	config.UseTestFile(t)
	testutils.NeedCouchdb(t)
	useCommandContexts(t)
	first := newInstance(t, commandContext, "en", "")
	org := "org-" + first.Domain
	first.OrgID = org
	// The instance helper registers cleanup; creation of the other members
	// uses the same org without assuming CouchDB's member ordering.
	require.NoError(t, couchdb.UpdateDoc(prefixer.GlobalPrefixer, first))
	newInstance(t, commandContext, "fr", org)
	newInstance(t, commandContext, "en", org)
	newInstance(t, commandContext, "en", org)
	members, err := lifecycle.ListOrgInstancesByID(org)
	require.NoError(t, err)
	require.Len(t, members, 4)
	cmd := fixture(t, "organization")
	cmd.OrgID = org
	failures := map[string]*atomic.Bool{}
	for _, inst := range members[1:3] {
		path := "/" + couchdb.EscapeCouchdbName(inst.DBPrefix()+"/"+consts.Banners) + "/banner-billing"
		failures[path] = &atomic.Bool{}
	}
	client := config.CouchClient()
	original := client.Transport
	t.Cleanup(func() { client.Transport = original })
	client.Transport = commandRoundTripper(func(r *http.Request) (*http.Response, error) {
		if failed := failures[r.URL.Path]; r.Method == http.MethodGet && failed != nil && failed.CompareAndSwap(false, true) {
			return nil, errors.New("simulated projection outage")
		}
		return original.RoundTrip(r)
	})
	err = banner.ApplyCommand(cmd)
	require.ErrorContains(t, err, "simulated projection outage")
	assert.NotErrorIs(t, err, banner.ErrInvalidCommand)
	for _, inst := range members[1:3] {
		assert.ErrorContains(t, err, inst.Domain, "every failed member must be reported")
	}
	before := storedBanner(t, members[0])
	require.NotNil(t, before)
	assert.Nil(t, storedBanner(t, members[1]))
	assert.Nil(t, storedBanner(t, members[2]))
	last := storedBanner(t, members[3])
	require.NotNil(t, last, "failures must not block later members")
	for _, inst := range members[1:3] {
		retained, err := banner.Stored(inst, banner.CategoryBilling)
		require.NoError(t, err)
		require.Nil(t, retained, "a failed member must not advance its revision")
	}
	require.NoError(t, banner.ApplyCommand(cmd))
	for _, inst := range members {
		require.NotNil(t, storedBanner(t, inst))
	}
	assert.Equal(t, before.DocRev, storedBanner(t, members[0]).DocRev)
	assert.Equal(t, last.DocRev, storedBanner(t, members[3]).DocRev)
}

func TestCommandClearRetriesProjectionFailure(t *testing.T) {
	config.UseTestFile(t)
	testutils.NeedCouchdb(t)
	useCommandContexts(t)
	inst := newInstance(t, commandContext, "en", "")
	old := materialize(t, inst, 1)
	require.NoError(t, banner.ApplyCommand(old))
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
	require.ErrorContains(t, banner.ApplyCommand(clear), "simulated clear outage")
	retained, err := banner.Stored(inst, banner.CategoryBilling)
	require.NoError(t, err)
	require.False(t, retained.Cleared, "a failed clear must leave the previous decision intact")
	require.Equal(t, int64(1), retained.Revision)
	require.NoError(t, banner.ApplyCommand(old))
	require.NoError(t, banner.ApplyCommand(clear))
	assert.Nil(t, storedBanner(t, inst))
}
