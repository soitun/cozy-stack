package sharings_test

import (
	"net/http"
	"testing"
)

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
