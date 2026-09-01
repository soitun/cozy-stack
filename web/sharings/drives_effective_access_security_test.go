package sharings_test

import (
	"net/http"
	"testing"

	"github.com/gavv/httpexpect/v2"
)

func TestSharedDriveWritableOpenSharecodeCannotWriteOutsideEffectiveScope(t *testing.T) {
	if testing.Short() {
		t.Skip("an instance is required for this test: test skipped due to the use of --short flag")
	}

	env := setupSharedDrivesEnv(t)
	eA, _, eD := env.createClients(t)

	// Dave can read D1 and write only inside its nested D2 scope.
	d1ID, d1RootID, _ := createSharedDrive(t, DriveCreationMethodFromFolder,
		env.acme, env.acmeToken, env.tsA.URL, "Open scope D1", "d1",
		[]RecipientInfo{{Name: "Dave", Email: "dave@example.net", ReadOnly: true}})
	writableDirID := createDirectory(t, eA, d1RootID, "Writable", env.acmeToken)
	rootOnlyFileID := createFile(t, eA, d1RootID, "read-only.txt", env.acmeToken)
	d2ID := createDriveOnFolder(t, eA, env.acme, writableDirID, env.acmeToken,
		[]RecipientInfo{{Name: "Dave", Email: "dave@example.net", ReadOnly: false}})

	noteID := eA.POST("/sharings/drives/"+d1ID+"/notes").
		WithHeader("Authorization", "Bearer "+env.acmeToken).
		WithHeader("Content-Type", "application/json").
		WithBytes([]byte(`{
			"data": {
				"type": "io.cozy.notes.documents",
				"attributes": {
					"title": "Writable note",
					"dir_id": "` + writableDirID + `",
					"schema": {
						"nodes": [
							["doc", {"content": "block+"}],
							["paragraph", {"content": "inline*", "group": "block"}],
							["text", {"group": "inline"}]
						],
						"marks": [],
						"topNode": "doc"
					}
				}
			}
		}`)).
		Expect().Status(http.StatusCreated).
		JSON(httpexpect.ContentOpts{MediaType: jsonAPIContentType}).
		Object().Path("$.data.id").String().NotEmpty().Raw()

	acceptSharedDrive(t, env.acme, env.dave, "Dave", env.tsA.URL, env.tsD.URL, d1ID)
	acceptSharedDrive(t, env.acme, env.dave, "Dave", env.tsA.URL, env.tsD.URL, d2ID)

	sharecode := eD.GET("/sharings/drives/"+d1ID+"/notes/"+noteID+"/open").
		WithHeader("Authorization", "Bearer "+env.daveToken).
		Expect().Status(http.StatusOK).
		JSON(httpexpect.ContentOpts{MediaType: jsonAPIContentType}).
		Object().Path("$.data.attributes.sharecode").String().NotEmpty().Raw()

	// The writable sharecode for the nested note must not grant write access
	// to a sibling where Dave only has D1's read-only access.
	eA.PUT("/files/"+rootOnlyFileID).
		WithHeader("Authorization", "Bearer "+sharecode).
		WithHeader("Content-Type", "text/plain").
		WithBytes([]byte("must not be writable")).
		Expect().Status(http.StatusForbidden)
}

func TestSharedDrivePendingNestedMemberDoesNotGrantEffectiveWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("an instance is required for this test: test skipped due to the use of --short flag")
	}

	env := setupSharedDrivesEnv(t)
	eA, _, eD := env.createClients(t)

	// Dave accepted read-only D1, but has not accepted the writable nested D2.
	d1ID, d1RootID, _ := createSharedDrive(t, DriveCreationMethodFromFolder,
		env.acme, env.acmeToken, env.tsA.URL, "Pending member D1", "d1",
		[]RecipientInfo{{Name: "Dave", Email: "dave@example.net", ReadOnly: true}})
	writableDirID := createDirectory(t, eA, d1RootID, "Pending writable", env.acmeToken)
	nestedFileID := createFile(t, eA, writableDirID, "pending.txt", env.acmeToken)
	createDriveOnFolder(t, eA, env.acme, writableDirID, env.acmeToken,
		[]RecipientInfo{{Name: "Dave", Email: "dave@example.net", ReadOnly: false}})
	acceptSharedDrive(t, env.acme, env.dave, "Dave", env.tsA.URL, env.tsD.URL, d1ID)

	eD.PATCH("/sharings/drives/"+d1ID+"/"+nestedFileID).
		WithHeader("Authorization", "Bearer "+env.daveToken).
		WithHeader("Content-Type", jsonAPIContentType).
		WithBytes([]byte(`{"data":{"type":"io.cozy.files","id":"` + nestedFileID + `","attributes":{"name":"must-not-change.txt"}}}`)).
		Expect().Status(http.StatusForbidden)
}
