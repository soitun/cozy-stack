package data

import (
	"testing"
	"time"

	"github.com/cozy/cozy-stack/pkg/config/config"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/tests/testutils"
	"github.com/stretchr/testify/require"
)

func TestBannerDismissalPreservesCommandState(t *testing.T) {
	if testing.Short() {
		t.Skip("requires CouchDB")
	}
	config.UseTestFile(t)
	testutils.NeedCouchdb(t)
	setup := testutils.NewSetup(t, t.Name())
	inst := setup.GetTestInstance()
	ts := setup.GetTestServer("/data", Routes)
	public := &couchdb.JSONDoc{Type: consts.Banners, M: M{
		"_id": "banner-billing", "revision": 42, "dismissedAt": nil,
		"accepted": M{"text": M{"en": "Payment failed", "fr": "Échec du paiement"}},
	}}
	require.NoError(t, couchdb.CreateNamedDocWithDB(inst, public))
	_, token := setup.GetTestClient(consts.Banners)
	e := testutils.CreateTestClient(t, ts.URL)
	path := "/data/" + consts.Banners + "/" + public.ID()
	e.GET(path).WithHeader("Authorization", "Bearer "+token).Expect().Status(200)
	previousRev := public.Rev()
	public.M["dismissedAt"] = time.Now().UTC().Format(time.RFC3339)
	e.PUT(path).WithHeader("Authorization", "Bearer "+token).WithJSON(public.M).Expect().Status(200)
	// The normal CouchDB revision check rejects a stale dismissal write.
	public.SetRev(previousRev)
	e.PUT(path).WithHeader("Authorization", "Bearer "+token).WithJSON(public.M).Expect().Status(409)
	var stored couchdb.JSONDoc
	require.NoError(t, couchdb.GetDoc(inst, consts.Banners, public.ID(), &stored))
	require.EqualValues(t, 42, stored.M["revision"])
	require.Equal(t, public.M["dismissedAt"], stored.M["dismissedAt"])
	require.Equal(t, "Échec du paiement", stored.M["accepted"].(map[string]interface{})["text"].(map[string]interface{})["fr"])
}
