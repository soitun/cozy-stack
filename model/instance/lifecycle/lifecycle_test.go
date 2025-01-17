package lifecycle_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/cozy/cozy-stack/model/bitwarden/settings"
	"github.com/cozy/cozy-stack/model/instance"
	"github.com/cozy/cozy-stack/model/instance/lifecycle"
	"github.com/cozy/cozy-stack/model/stack"
	"github.com/cozy/cozy-stack/model/vfs"
	"github.com/cozy/cozy-stack/pkg/config/config"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/couchdb/mango"
	"github.com/cozy/cozy-stack/pkg/prefixer"
	"github.com/cozy/cozy-stack/tests/testutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/cozy/cozy-stack/worker/mails"
)

func TestLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("an instance is required for this test: test skipped due to the use of --short flag")
	}

	config.UseTestFile()

	testutils.NeedCouchdb(t)

	_, err := stack.Start()
	require.NoError(t, err)

	t.Cleanup(cleanInstance)

	t.Run("ChooseCouchCluster", func(t *testing.T) {
		clusters := []config.CouchDBCluster{
			{Creation: false},
		}
		_, err := lifecycle.ChooseCouchCluster(clusters)
		assert.Error(t, err)

		clusters = []config.CouchDBCluster{
			{Creation: false},
			{Creation: true},
		}
		index, err := lifecycle.ChooseCouchCluster(clusters)
		assert.NoError(t, err)
		assert.Equal(t, 1, index)

		clusters = []config.CouchDBCluster{
			{Creation: false},
			{Creation: true},
			{Creation: true},
			{Creation: false},
			{Creation: true},
		}
		counts := make([]int, len(clusters))
		for i := 0; i < 10000; i++ {
			index, err = lifecycle.ChooseCouchCluster(clusters)
			assert.NoError(t, err)
			counts[index] += 1
		}
		assert.Equal(t, 0, counts[0])
		assert.Greater(t, counts[1], 3000)
		assert.Greater(t, counts[2], 3000)
		assert.Equal(t, 0, counts[3])
		assert.Greater(t, counts[4], 3000)
	})

	t.Run("GetInstanceNoDB", func(t *testing.T) {
		res, err := lifecycle.GetInstance("no.instance.cozycloud.cc")
		if assert.Error(t, err, "An error is expected") {
			assert.Nil(t, res)
			assert.ErrorIs(t, err, instance.ErrNotFound)
		}
	})

	t.Run("CreateInstance", func(t *testing.T) {
		instance, err := lifecycle.Create(&lifecycle.Options{
			Domain: "test.cozycloud.cc",
			Locale: "en",
		})
		if assert.NoError(t, err) {
			assert.NotEmpty(t, instance.ID())
			assert.Equal(t, instance.Domain, "test.cozycloud.cc")
		}
	})

	t.Run("CreateInstanceWithFewSettings", func(t *testing.T) {
		inst, err := lifecycle.Create(&lifecycle.Options{
			Domain:     "test2.cozycloud.cc",
			Timezone:   "Europe/Berlin",
			Email:      "alice@example.com",
			PublicName: "Alice",
			Passphrase: "password",
			Settings:   "offer:freemium,context:my_context,auth_mode:two_factor_mail,uuid:XXX,locale:en,tos:20151111,oidc_id:oidc_42",
		})

		assert.NoError(t, err)
		assert.Equal(t, inst.Domain, "test2.cozycloud.cc")
		doc, err := inst.SettingsDocument()
		assert.NoError(t, err)
		assert.Equal(t, "Europe/Berlin", doc.M["tz"].(string))
		assert.Equal(t, "alice@example.com", doc.M["email"].(string))
		assert.Equal(t, "freemium", doc.M["offer"].(string))
		assert.Equal(t, "Alice", doc.M["public_name"].(string))

		assert.Equal(t, inst.UUID, "XXX")
		assert.Equal(t, inst.OIDCID, "oidc_42")
		assert.Equal(t, inst.Locale, "en")
		assert.Equal(t, inst.TOSSigned, "1.0.0-20151111")
		assert.Equal(t, inst.ContextName, "my_context")
		assert.Equal(t, inst.AuthMode, instance.TwoFactorMail)
	})

	t.Run("CreateInstanceWithMoreSettings", func(t *testing.T) {
		inst, err := lifecycle.Create(&lifecycle.Options{
			Domain:      "test3.cozycloud.cc",
			UUID:        "XXX",
			OIDCID:      "oidc_42",
			Locale:      "en",
			TOSSigned:   "20151111",
			TOSLatest:   "1.0.0-20151111",
			Timezone:    "Europe/Berlin",
			ContextName: "my_context",
			Email:       "alice@example.com",
			PublicName:  "Alice",
			AuthMode:    "two_factor_mail",
			Passphrase:  "password",
			Settings:    "offer:freemium",
		})

		assert.NoError(t, err)
		assert.Equal(t, inst.Domain, "test3.cozycloud.cc")
		doc, err := inst.SettingsDocument()
		assert.NoError(t, err)
		assert.Equal(t, "Europe/Berlin", doc.M["tz"].(string))
		assert.Equal(t, "alice@example.com", doc.M["email"].(string))
		assert.Equal(t, "freemium", doc.M["offer"].(string))
		assert.Equal(t, "Alice", doc.M["public_name"].(string))

		assert.Equal(t, inst.UUID, "XXX")
		assert.Equal(t, inst.OIDCID, "oidc_42")
		assert.Equal(t, inst.Locale, "en")
		assert.Equal(t, inst.TOSSigned, "1.0.0-20151111")
		assert.Equal(t, inst.ContextName, "my_context")
		assert.Equal(t, inst.AuthMode, instance.TwoFactorMail)
	})

	t.Run("CreateInstanceBadDomain", func(t *testing.T) {
		_, err := lifecycle.Create(&lifecycle.Options{
			Domain: "..",
			Locale: "en",
		})
		assert.Error(t, err, "An error is expected")

		_, err = lifecycle.Create(&lifecycle.Options{
			Domain: ".",
			Locale: "en",
		})
		assert.Error(t, err, "An error is expected")

		_, err = lifecycle.Create(&lifecycle.Options{
			Domain: "foo/bar",
			Locale: "en",
		})
		assert.Error(t, err, "An error is expected")
	})

	t.Run("GetWrongInstance", func(t *testing.T) {
		res, err := lifecycle.GetInstance("no.instance.cozycloud.cc")
		if assert.Error(t, err, "An error is expected") {
			assert.Nil(t, res)
			assert.ErrorIs(t, err, instance.ErrNotFound)
		}
	})

	t.Run("GetCorrectInstance", func(t *testing.T) {
		instance, err := lifecycle.GetInstance("test.cozycloud.cc")
		if assert.NoError(t, err) {
			assert.NotNil(t, instance)
			assert.Equal(t, instance.Domain, "test.cozycloud.cc")
		}
	})

	t.Run("InstancehasOAuthSecret", func(t *testing.T) {
		i, err := lifecycle.GetInstance("test.cozycloud.cc")
		if assert.NoError(t, err) {
			assert.NotNil(t, i)
			assert.NotNil(t, i.OAuthSecret)
			assert.Equal(t, len(i.OAuthSecret), instance.OauthSecretLen)
		}
	})

	t.Run("InstanceHasRootDir", func(t *testing.T) {
		var root vfs.DirDoc
		prefix := getDB(t, "test.cozycloud.cc")
		err := couchdb.GetDoc(prefix, consts.Files, consts.RootDirID, &root)
		if assert.NoError(t, err) {
			assert.Equal(t, root.Fullpath, "/")
		}
	})

	t.Run("InstanceHasIndexes", func(t *testing.T) {
		var results []*vfs.DirDoc
		prefix := getDB(t, "test.cozycloud.cc")
		req := &couchdb.FindRequest{Selector: mango.Equal("path", "/")}
		err := couchdb.FindDocs(prefix, consts.Files, req, &results)
		assert.NoError(t, err)
		assert.Len(t, results, 1)
	})

	t.Run("RegisterPassphrase", func(t *testing.T) {
		i, err := lifecycle.GetInstance("test.cozycloud.cc")
		if !assert.NoError(t, err, "cant fetch i") {
			return
		}
		assert.NotNil(t, i)
		assert.NotEmpty(t, i.RegisterToken)
		assert.Len(t, i.RegisterToken, instance.RegisterTokenLen)
		assert.NotEmpty(t, i.OAuthSecret)
		assert.Len(t, i.OAuthSecret, instance.OauthSecretLen)
		assert.NotEmpty(t, i.SessSecret)
		assert.Len(t, i.SessSecret, instance.SessionSecretLen)

		rtoken := i.RegisterToken
		pass := []byte("passphrase")
		empty := []byte("")
		badtoken := []byte("not-token")

		err = lifecycle.RegisterPassphrase(i, empty, lifecycle.PassParameters{
			Pass:       pass,
			Iterations: 5000,
			Key:        "0.uRcMe+Mc2nmOet4yWx9BwA==|PGQhpYUlTUq/vBEDj1KOHVMlTIH1eecMl0j80+Zu0VRVfFa7X/MWKdVM6OM/NfSZicFEwaLWqpyBlOrBXhR+trkX/dPRnfwJD2B93hnLNGQ=",
		})
		assert.Error(t, err, "RegisterPassphrase requires token")

		err = lifecycle.RegisterPassphrase(i, badtoken, lifecycle.PassParameters{
			Pass:       pass,
			Iterations: 5000,
			Key:        "0.uRcMe+Mc2nmOet4yWx9BwA==|PGQhpYUlTUq/vBEDj1KOHVMlTIH1eecMl0j80+Zu0VRVfFa7X/MWKdVM6OM/NfSZicFEwaLWqpyBlOrBXhR+trkX/dPRnfwJD2B93hnLNGQ=",
		})
		assert.Error(t, err, "RegisterPassphrase requires proper token")

		err = lifecycle.RegisterPassphrase(i, rtoken, lifecycle.PassParameters{
			Pass:       pass,
			Iterations: 5000,
			Key:        "0.uRcMe+Mc2nmOet4yWx9BwA==|PGQhpYUlTUq/vBEDj1KOHVMlTIH1eecMl0j80+Zu0VRVfFa7X/MWKdVM6OM/NfSZicFEwaLWqpyBlOrBXhR+trkX/dPRnfwJD2B93hnLNGQ=",
		})
		assert.NoError(t, err)

		assert.Empty(t, i.RegisterToken, "RegisterToken has not been removed")
		assert.NotEmpty(t, i.PassphraseHash, "PassphraseHash has not been saved")

		err = lifecycle.RegisterPassphrase(i, rtoken, lifecycle.PassParameters{
			Pass:       pass,
			Iterations: 5000,
			Key:        "0.uRcMe+Mc2nmOet4yWx9BwA==|PGQhpYUlTUq/vBEDj1KOHVMlTIH1eecMl0j80+Zu0VRVfFa7X/MWKdVM6OM/NfSZicFEwaLWqpyBlOrBXhR+trkX/dPRnfwJD2B93hnLNGQ=",
		})
		assert.Error(t, err, "RegisterPassphrase works only once")
	})

	t.Run("UpdatePassphrase", func(t *testing.T) {
		i, err := lifecycle.GetInstance("test.cozycloud.cc")
		if !assert.NoError(t, err, "cant fetch i") {
			return
		}
		assert.NotNil(t, i)
		assert.Empty(t, i.RegisterToken)
		assert.NotEmpty(t, i.OAuthSecret)
		assert.Len(t, i.OAuthSecret, instance.OauthSecretLen)
		assert.NotEmpty(t, i.SessSecret)
		assert.Len(t, i.SessSecret, instance.SessionSecretLen)

		oldHash := i.PassphraseHash
		oldSecret := i.SessSecret

		currentPass := []byte("passphrase")
		newPass := []byte("new-passphrase")
		badPass := []byte("not-passphrase")
		empty := []byte("")

		params := lifecycle.PassParameters{
			Pass:       newPass,
			Iterations: 5000,
		}
		err = lifecycle.UpdatePassphrase(i, empty, "", nil, params)
		assert.Error(t, err, "UpdatePassphrase requires the current passphrase")

		err = lifecycle.UpdatePassphrase(i, badPass, "", nil, params)
		assert.Error(t, err, "UpdatePassphrase requires the current passphrase")

		err = lifecycle.UpdatePassphrase(i, currentPass, "", nil, params)
		assert.NoError(t, err)

		assert.NotEmpty(t, i.PassphraseHash, "PassphraseHash has not been saved")
		assert.NotEqual(t, oldHash, i.PassphraseHash)
		assert.NotEqual(t, oldSecret, i.SessSecret)

		settings, err := settings.Get(i)
		assert.NoError(t, err)
		assert.Equal(t, 5000, settings.PassphraseKdfIterations)
		assert.Equal(t, 0, settings.PassphraseKdf)
	})

	t.Run("RequestPassphraseReset", func(t *testing.T) {
		in, err := lifecycle.Create(&lifecycle.Options{
			Domain: "test.cozycloud.cc.pass_reset",
			Locale: "en",
		})
		require.NoError(t, err)

		err = lifecycle.RequestPassphraseReset(in)
		require.NoError(t, err)

		// token should not have been generated since we have not set a passphrase
		// yet
		require.Nil(t, in.PassphraseResetToken)

		err = lifecycle.RegisterPassphrase(in, in.RegisterToken, lifecycle.PassParameters{
			Pass:       []byte("MyPassphrase"),
			Iterations: 5000,
			Key:        "0.uRcMe+Mc2nmOet4yWx9BwA==|PGQhpYUlTUq/vBEDj1KOHVMlTIH1eecMl0j80+Zu0VRVfFa7X/MWKdVM6OM/NfSZicFEwaLWqpyBlOrBXhR+trkX/dPRnfwJD2B93hnLNGQ=",
		})
		require.NoError(t, err)

		err = lifecycle.RequestPassphraseReset(in)
		require.NoError(t, err)

		regToken := in.PassphraseResetToken
		regTime := in.PassphraseResetTime
		assert.NotNil(t, in.PassphraseResetToken)
		assert.True(t, !in.PassphraseResetTime.Before(time.Now().UTC()))

		err = lifecycle.RequestPassphraseReset(in)
		assert.Equal(t, instance.ErrResetAlreadyRequested, err)
		assert.EqualValues(t, regToken, in.PassphraseResetToken)
		assert.EqualValues(t, regTime, in.PassphraseResetTime)
	})

	t.Run("PassphraseRenew", func(t *testing.T) {
		in, err := lifecycle.Create(&lifecycle.Options{
			Domain: "test.cozycloud.cc.pass_renew",
			Locale: "en",
		})
		require.NoError(t, err)

		err = lifecycle.RegisterPassphrase(in, in.RegisterToken, lifecycle.PassParameters{
			Pass:       []byte("MyPassphrase"),
			Iterations: 5000,
			Key:        "0.uRcMe+Mc2nmOet4yWx9BwA==|PGQhpYUlTUq/vBEDj1KOHVMlTIH1eecMl0j80+Zu0VRVfFa7X/MWKdVM6OM/NfSZicFEwaLWqpyBlOrBXhR+trkX/dPRnfwJD2B93hnLNGQ=",
		})
		require.NoError(t, err)

		passHash := in.PassphraseHash
		err = lifecycle.PassphraseRenew(in, nil, lifecycle.PassParameters{
			Pass:       []byte("NewPass"),
			Iterations: 5000,
			Key:        "0.uRcMe+Mc2nmOet4yWx9BwA==|PGQhpYUlTUq/vBEDj1KOHVMlTIH1eecMl0j80+Zu0VRVfFa7X/MWKdVM6OM/NfSZicFEwaLWqpyBlOrBXhR+trkX/dPRnfwJD2B93hnLNGQ=",
		})
		require.Error(t, err)

		err = lifecycle.RequestPassphraseReset(in)
		require.NoError(t, err)

		err = lifecycle.PassphraseRenew(in, []byte("token"), lifecycle.PassParameters{
			Pass:       []byte("NewPass"),
			Iterations: 5000,
			Key:        "0.uRcMe+Mc2nmOet4yWx9BwA==|PGQhpYUlTUq/vBEDj1KOHVMlTIH1eecMl0j80+Zu0VRVfFa7X/MWKdVM6OM/NfSZicFEwaLWqpyBlOrBXhR+trkX/dPRnfwJD2B93hnLNGQ=",
		})
		require.Error(t, err)

		err = lifecycle.PassphraseRenew(in, in.PassphraseResetToken, lifecycle.PassParameters{
			Pass:       []byte("NewPass"),
			Iterations: 5000,
			Key:        "0.uRcMe+Mc2nmOet4yWx9BwA==|PGQhpYUlTUq/vBEDj1KOHVMlTIH1eecMl0j80+Zu0VRVfFa7X/MWKdVM6OM/NfSZicFEwaLWqpyBlOrBXhR+trkX/dPRnfwJD2B93hnLNGQ=",
		})
		require.NoError(t, err)

		assert.False(t, bytes.Equal(passHash, in.PassphraseHash))
	})

	t.Run("InstanceNoDuplicate", func(t *testing.T) {
		_, err := lifecycle.Create(&lifecycle.Options{
			Domain: "test.cozycloud.cc.duplicate",
			Locale: "en",
		})
		require.NoError(t, err)

		i, err := lifecycle.Create(&lifecycle.Options{
			Domain: "test.cozycloud.cc.duplicate",
			Locale: "en",
		})
		if assert.Error(t, err, "Should not be possible to create duplicate") {
			assert.Nil(t, i)
			assert.ErrorIs(t, err, instance.ErrExists)
		}
	})

	t.Run("CheckPassphrase", func(t *testing.T) {
		inst, err := lifecycle.GetInstance("test.cozycloud.cc")
		if !assert.NoError(t, err, "cant fetch instance") {
			return
		}

		assert.Empty(t, inst.RegisterToken, "changes have been saved in db")
		assert.NotEmpty(t, inst.PassphraseHash, "changes have been saved in db")

		err = lifecycle.CheckPassphrase(inst, []byte("not-passphrase"))
		assert.Error(t, err)

		err = lifecycle.CheckPassphrase(inst, []byte("new-passphrase"))
		assert.NoError(t, err)
	})

	t.Run("CheckTOSNotSigned", func(t *testing.T) {
		now := time.Now()
		i, err := lifecycle.Create(&lifecycle.Options{
			Domain:    "tos.test.cozycloud.cc",
			Locale:    "en",
			TOSSigned: "1.0.0-" + now.Format("20060102"),
		})
		require.NoError(t, err)

		notSigned, deadline := i.CheckTOSNotSignedAndDeadline()
		assert.Empty(t, i.TOSLatest)
		assert.False(t, notSigned)
		assert.Equal(t, instance.TOSNone, deadline)

		err = lifecycle.Patch(i, &lifecycle.Options{
			TOSLatest: "1.0.1-" + now.Format("20060102"),
		})
		require.NoError(t, err)

		notSigned, deadline = i.CheckTOSNotSignedAndDeadline()
		assert.Empty(t, i.TOSLatest)
		assert.False(t, notSigned)
		assert.Equal(t, instance.TOSNone, deadline)

		err = lifecycle.Patch(i, &lifecycle.Options{
			TOSLatest: "2.0.1-" + now.Add(40*24*time.Hour).Format("20060102"),
		})
		require.NoError(t, err)

		notSigned, deadline = i.CheckTOSNotSignedAndDeadline()
		assert.NotEmpty(t, i.TOSLatest)
		assert.True(t, notSigned)
		assert.Equal(t, instance.TOSNone, deadline)

		err = lifecycle.Patch(i, &lifecycle.Options{
			TOSLatest: "2.0.1-" + now.Add(10*24*time.Hour).Format("20060102"),
		})
		require.NoError(t, err)

		notSigned, deadline = i.CheckTOSNotSignedAndDeadline()
		assert.NotEmpty(t, i.TOSLatest)
		assert.True(t, notSigned)
		assert.Equal(t, instance.TOSWarning, deadline)

		err = lifecycle.Patch(i, &lifecycle.Options{
			TOSLatest: "2.0.1-" + now.Format("20060102"),
		})
		require.NoError(t, err)

		notSigned, deadline = i.CheckTOSNotSignedAndDeadline()
		assert.NotEmpty(t, i.TOSLatest)
		assert.True(t, notSigned)
		assert.Equal(t, instance.TOSBlocked, deadline)

		err = lifecycle.Patch(i, &lifecycle.Options{
			TOSSigned: "2.0.1-" + now.Format("20060102"),
		})
		require.NoError(t, err)

		notSigned, deadline = i.CheckTOSNotSignedAndDeadline()
		assert.Empty(t, i.TOSLatest)
		assert.False(t, notSigned)
		assert.Equal(t, instance.TOSNone, deadline)
	})

	t.Run("InstanceDestroy", func(t *testing.T) {
		_ = lifecycle.Destroy("test.cozycloud.cc")

		_, err := lifecycle.Create(&lifecycle.Options{
			Domain: "test.cozycloud.cc",
			Locale: "en",
		})
		require.NoError(t, err)

		err = lifecycle.Destroy("test.cozycloud.cc")
		assert.NoError(t, err)

		err = lifecycle.Destroy("test.cozycloud.cc")
		if assert.Error(t, err) {
			assert.Equal(t, instance.ErrNotFound, err)
		}
	})
}

func cleanInstance() {
	_ = lifecycle.Destroy("test.cozycloud.cc")
	_ = lifecycle.Destroy("test2.cozycloud.cc")
	_ = lifecycle.Destroy("test3.cozycloud.cc")
	_ = lifecycle.Destroy("test.cozycloud.cc.pass_reset")
	_ = lifecycle.Destroy("test.cozycloud.cc.pass_renew")
	_ = lifecycle.Destroy("test.cozycloud.cc.duplicate")
	_ = lifecycle.Destroy("tos.test.cozycloud.cc")
}

func getDB(t *testing.T, domain string) prefixer.Prefixer {
	instance, err := lifecycle.GetInstance(domain)
	if !assert.NoError(t, err, "Should get instance %v", domain) {
		t.FailNow()
	}
	return instance
}
