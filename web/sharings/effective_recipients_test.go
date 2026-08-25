package sharings_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/cozy/cozy-stack/model/instance"
	"github.com/cozy/cozy-stack/model/permission"
	"github.com/cozy/cozy-stack/model/sharing"
	"github.com/cozy/cozy-stack/model/vfs"
	"github.com/cozy/cozy-stack/pkg/config/config"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/tests/testutils"
	"github.com/cozy/cozy-stack/web/errors"
	"github.com/cozy/cozy-stack/web/middlewares"
	"github.com/cozy/cozy-stack/web/sharings"
	"github.com/cozy/cozy-stack/web/statik"
	"github.com/gavv/httpexpect/v2"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

type effectiveRecipientsEnv struct {
	inst  *instance.Instance
	token string
	e     *httpexpect.Expect
}

func setupEffectiveRecipientsEnv(t *testing.T, doctype string) *effectiveRecipientsEnv {
	t.Helper()
	config.UseTestFile(t)
	testutils.NeedCouchdb(t)
	render, _ := statik.NewDirRenderer("../../assets")
	middlewares.BuildTemplates()

	setup := testutils.NewSetup(t, t.Name())
	inst := setup.GetTestInstance()
	token := generateAppToken(inst, "testapp", doctype)
	ts := setup.GetTestServerMultipleRoutes(map[string]func(*echo.Group){
		"/sharings": sharings.Routes,
	})
	ts.Config.Handler.(*echo.Echo).Renderer = render
	ts.Config.Handler.(*echo.Echo).HTTPErrorHandler = errors.ErrorHandler
	t.Cleanup(ts.Close)

	return &effectiveRecipientsEnv{
		inst:  inst,
		token: token,
		e:     httpexpect.Default(t, ts.URL),
	}
}

func createTestDir(t *testing.T, inst *instance.Instance, name, parentID string) *vfs.DirDoc {
	t.Helper()
	dir, err := vfs.NewDirDoc(inst.VFS(), name, parentID, nil)
	require.NoError(t, err)
	require.NoError(t, inst.VFS().CreateDir(dir))
	return dir
}

// createTestSharing persists an active additive sharing on the given root,
// owned by the instance, with Bob as an extra member.
func createTestSharing(t *testing.T, inst *instance.Instance, rootID string, drive bool) *sharing.Sharing {
	t.Helper()
	now := time.Now()
	s := &sharing.Sharing{
		Active:        true,
		Owner:         true,
		Drive:         drive,
		DriveRootType: sharing.DriveRootTypeDirectory,
		AppSlug:       "test",
		AccessMode:    sharing.AccessModeAdditive,
		Members: []sharing.Member{
			{
				Status:   sharing.MemberStatusOwner,
				Name:     "Owner",
				Email:    "owner@example.net",
				Instance: "https://" + inst.Domain,
			},
			{
				Status:   sharing.MemberStatusReady,
				Name:     "Bob",
				Email:    "bob@example.net",
				Instance: "https://bob.example.net",
			},
		},
		Rules: []sharing.Rule{
			{
				Title:   "test",
				DocType: consts.Files,
				Values:  []string{rootID},
			},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	require.NoError(t, couchdb.CreateDoc(inst, s))
	require.NoError(t, s.AddReferenceForSharing(inst, &s.Rules[0]))
	return s
}

func TestEffectiveRecipientsEndpoint_OK(t *testing.T) {
	if testing.Short() {
		t.Skip("an instance is required for this test: test skipped due to the use of --short flag")
	}
	env := setupEffectiveRecipientsEnv(t, consts.Files)

	parent := createTestDir(t, env.inst, "parent", consts.RootDirID)
	createTestSharing(t, env.inst, parent.ID(), false)

	obj := env.e.GET("/sharings/recipients/"+parent.ID()).
		WithHeader("Authorization", "Bearer "+env.token).
		Expect().Status(http.StatusOK).
		JSON(httpexpect.ContentOpts{MediaType: "application/vnd.api+json"}).
		Object()

	obj.Path("$.meta.file_id").String().IsEqual(parent.ID())
	data := obj.Value("data").Array()
	data.Length().IsEqual(2)
	found := false
	for _, v := range data.Iter() {
		attrs := v.Object().Value("attributes").Object()
		if attrs.Value("email").String().Raw() == "bob@example.net" {
			found = true
			attrs.Value("name").String().IsEqual("Bob")
			attrs.Value("can_edit_here").Boolean().IsTrue()
			attrs.Value("read_only").Boolean().IsFalse()
			sources := attrs.Value("sources").Array()
			sources.Length().IsEqual(1)
			sources.First().Object().Value("kind").String().IsEqual("self")
		}
	}
	require.True(t, found, "bob should be in the recipients list")
}

func TestEffectiveRecipientsEndpoint_Forbidden(t *testing.T) {
	if testing.Short() {
		t.Skip("an instance is required for this test: test skipped due to the use of --short flag")
	}
	env := setupEffectiveRecipientsEnv(t, iocozytests)

	parent := createTestDir(t, env.inst, "parent", consts.RootDirID)

	env.e.GET("/sharings/recipients/"+parent.ID()).
		WithHeader("Authorization", "Bearer "+env.token).
		Expect().Status(http.StatusForbidden)
}

// A public share-by-link token grants read access to the file, but it must
// not allow enumerating the members of the sharings applying to it.
func TestEffectiveRecipientsEndpoint_PublicLinkForbidden(t *testing.T) {
	if testing.Short() {
		t.Skip("an instance is required for this test: test skipped due to the use of --short flag")
	}
	env := setupEffectiveRecipientsEnv(t, consts.Files)

	parent := createTestDir(t, env.inst, "parent", consts.RootDirID)
	createTestSharing(t, env.inst, parent.ID(), false)

	publicToken, err := env.inst.MakeJWT(consts.ShareAudience, "email", consts.Files, "", time.Now())
	require.NoError(t, err)
	expires := time.Now().Add(2 * time.Minute)
	rules := permission.Set{permission.Rule{
		Type:   consts.Files,
		Verbs:  permission.Verbs(permission.GET),
		Values: []string{parent.ID()},
	}}
	_, err = permission.CreateShareSet(env.inst,
		&permission.Permission{Type: "app", Permissions: rules},
		"", map[string]string{"email": publicToken}, nil,
		permission.Permission{Permissions: rules}, &expires, false)
	require.NoError(t, err)

	env.e.GET("/sharings/recipients/"+parent.ID()).
		WithHeader("Authorization", "Bearer "+publicToken).
		Expect().Status(http.StatusForbidden)
}

func TestEffectiveRecipientsEndpoint_NotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("an instance is required for this test: test skipped due to the use of --short flag")
	}
	env := setupEffectiveRecipientsEnv(t, consts.Files)

	env.e.GET("/sharings/recipients/does-not-exist").
		WithHeader("Authorization", "Bearer "+env.token).
		Expect().Status(http.StatusNotFound)
}

func TestDriveEffectiveRecipientsEndpoint_OK(t *testing.T) {
	if testing.Short() {
		t.Skip("an instance is required for this test: test skipped due to the use of --short flag")
	}
	env := setupEffectiveRecipientsEnv(t, consts.Files)

	root := createTestDir(t, env.inst, "drive-root", consts.RootDirID)
	child := createTestDir(t, env.inst, "child", root.ID())
	s := createTestSharing(t, env.inst, root.ID(), true)

	obj := env.e.GET("/sharings/drives/"+s.SID+"/recipients/"+child.ID()).
		WithHeader("Authorization", "Bearer "+env.token).
		Expect().Status(http.StatusOK).
		JSON(httpexpect.ContentOpts{MediaType: "application/vnd.api+json"}).
		Object()

	obj.Path("$.meta.file_id").String().IsEqual(child.ID())
	data := obj.Value("data").Array()
	data.Length().IsEqual(2)
	for _, v := range data.Iter() {
		attrs := v.Object().Value("attributes").Object()
		if attrs.Value("email").String().Raw() == "bob@example.net" {
			attrs.Value("sources").Array().First().Object().Value("kind").String().IsEqual("ancestor")
			attrs.Value("can_edit_here").Boolean().IsFalse()
			return
		}
	}
	t.Fatal("bob should be in the recipients list")
}

func TestDriveEffectiveRecipientsEndpoint_OutsideDrive(t *testing.T) {
	if testing.Short() {
		t.Skip("an instance is required for this test: test skipped due to the use of --short flag")
	}
	env := setupEffectiveRecipientsEnv(t, consts.Files)

	root := createTestDir(t, env.inst, "drive-root", consts.RootDirID)
	outside := createTestDir(t, env.inst, "outside", consts.RootDirID)
	s := createTestSharing(t, env.inst, root.ID(), true)

	env.e.GET("/sharings/drives/"+s.SID+"/recipients/"+outside.ID()).
		WithHeader("Authorization", "Bearer "+env.token).
		Expect().Status(http.StatusNotFound)
}

func TestDriveEffectiveRecipientsEndpoint_InactiveDrive(t *testing.T) {
	if testing.Short() {
		t.Skip("an instance is required for this test: test skipped due to the use of --short flag")
	}
	env := setupEffectiveRecipientsEnv(t, consts.Files)

	root := createTestDir(t, env.inst, "drive-root", consts.RootDirID)
	s := createTestSharing(t, env.inst, root.ID(), true)
	// A revoked drive is rejected, like on the other drive endpoints.
	s.Active = false
	require.NoError(t, couchdb.UpdateDoc(env.inst, s))

	env.e.GET("/sharings/drives/"+s.SID+"/recipients/"+root.ID()).
		WithHeader("Authorization", "Bearer "+env.token).
		Expect().Status(http.StatusForbidden)
}

func TestDriveEffectiveRecipientsEndpoint_FileDriveRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("an instance is required for this test: test skipped due to the use of --short flag")
	}
	env := setupEffectiveRecipientsEnv(t, consts.Files)

	filedoc, err := vfs.NewFileDoc("shared-file", consts.RootDirID, -1, nil, "text/plain", "text", time.Now(), false, false, false, nil)
	require.NoError(t, err)
	f, err := env.inst.VFS().CreateFile(filedoc, nil)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	s := createTestSharing(t, env.inst, filedoc.ID(), true)
	s.DriveRootType = sharing.DriveRootTypeFile
	require.NoError(t, couchdb.UpdateDoc(env.inst, s))

	env.e.GET("/sharings/drives/"+s.SID+"/recipients/"+filedoc.ID()).
		WithHeader("Authorization", "Bearer "+env.token).
		Expect().Status(http.StatusOK)

	other := createTestDir(t, env.inst, "other", consts.RootDirID)
	env.e.GET("/sharings/drives/"+s.SID+"/recipients/"+other.ID()).
		WithHeader("Authorization", "Bearer "+env.token).
		Expect().Status(http.StatusNotFound)
}
