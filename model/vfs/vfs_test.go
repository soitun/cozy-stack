package vfs_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/cozy/cozy-stack/model/vfs"
	"github.com/cozy/cozy-stack/model/vfs/vfsafero"
	"github.com/cozy/cozy-stack/model/vfs/vfsswift"
	"github.com/cozy/cozy-stack/pkg/config/config"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/crypto"
	"github.com/cozy/cozy-stack/pkg/lock"
	"github.com/ncw/swift/v2/swifttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

var fs vfs.VFS
var mutex lock.ErrorRWLocker
var diskQuota int64

type diskImpl struct{}

func (d *diskImpl) DiskQuota() int64 {
	return diskQuota
}

type H map[string]H

func (h H) String() string {
	return printH(h, "", 0)
}

func printH(h H, str string, count int) string {
	for name, hh := range h {
		for i := 0; i < count; i++ {
			str += "\t"
		}
		str += fmt.Sprintf("%s:\n", name)
		str += printH(hh, "", count+1)
	}
	return str
}

func createTree(tree H, dirID string) (*vfs.DirDoc, error) {
	if tree == nil {
		return nil, nil
	}

	if dirID == "" {
		dirID = consts.RootDirID
	}

	var err error
	var dirdoc *vfs.DirDoc
	for name, children := range tree {
		if name[len(name)-1] == '/' {
			dirdoc, err = vfs.NewDirDoc(fs, name[:len(name)-1], dirID, nil)
			if err != nil {
				return nil, err
			}
			if err = fs.CreateDir(dirdoc); err != nil {
				return nil, err
			}
			if _, err = createTree(children, dirdoc.ID()); err != nil {
				return nil, err
			}
		} else {
			mime, class := vfs.ExtractMimeAndClassFromFilename(name)
			filedoc, err := vfs.NewFileDoc(name, dirID, -1, nil, mime, class, time.Now(), false, false, false, nil)
			if err != nil {
				return nil, err
			}
			f, err := fs.CreateFile(filedoc, nil)
			if err != nil {
				return nil, err
			}
			if err = f.Close(); err != nil {
				return nil, err
			}
		}
	}
	return dirdoc, nil
}

func fetchTree(root string) (H, error) {
	parent, err := fs.DirByPath(root)
	if err != nil {
		return nil, err
	}
	h, err := recFetchTree(parent, path.Clean(root))
	if err != nil {
		return nil, err
	}
	hh := make(H)
	hh[parent.DocName+"/"] = h
	return hh, nil
}

func recFetchTree(parent *vfs.DirDoc, name string) (H, error) {
	h := make(H)
	iter := fs.DirIterator(parent, nil)
	for {
		d, f, err := iter.Next()
		if err == vfs.ErrIteratorDone {
			break
		}
		if err != nil {
			return nil, err
		}
		if d != nil {
			if path.Join(name, d.DocName) != d.Fullpath {
				return nil, fmt.Errorf("Bad fullpath: %s instead of %s", d.Fullpath, path.Join(name, d.DocName))
			}
			children, err := recFetchTree(d, d.Fullpath)
			if err != nil {
				return nil, err
			}
			h[d.DocName+"/"] = children
		} else {
			h[f.DocName] = nil
		}
	}
	return h, nil
}

func TestDiskUsageIsInitiallyZero(t *testing.T) {
	used, err := fs.DiskUsage()
	assert.NoError(t, err)
	assert.Equal(t, int64(0), used)
}

func TestGetFileDocFromPathAtRoot(t *testing.T) {
	doc, err := vfs.NewFileDoc("toto", "", -1, nil, "foo/bar", "foo", time.Now(), false, false, false, []string{})
	assert.NoError(t, err)

	body := bytes.NewReader([]byte("hello !"))

	file, err := fs.CreateFile(doc, nil)
	assert.NoError(t, err)

	n, err := io.Copy(file, body)
	assert.NoError(t, err)
	assert.Equal(t, len("hello !"), int(n))

	err = file.Close()
	assert.NoError(t, err)

	_, err = fs.FileByPath("/toto")
	assert.NoError(t, err)

	_, err = fs.FileByPath("/noooo")
	assert.Error(t, err)
}

func TestRemove(t *testing.T) {
	err := vfs.Remove(fs, "foo/bar", fs.EnsureErased)
	assert.Error(t, err)
	assert.Equal(t, vfs.ErrNonAbsolutePath, err)

	err = vfs.Remove(fs, "/foo", fs.EnsureErased)
	assert.Error(t, err)
	assert.Equal(t, "file does not exist", err.Error())

	_, err = vfs.Mkdir(fs, "/removeme", nil)
	if !assert.NoError(t, err) {
		err = vfs.Remove(fs, "/removeme", fs.EnsureErased)
		assert.NoError(t, err)
	}
}

func TestRemoveAll(t *testing.T) {
	origtree := H{
		"removemeall/": H{
			"dirchild1/": H{
				"food/": H{},
				"bard/": H{},
			},
			"dirchild2/": H{
				"foof": nil,
				"barf": nil,
			},
			"dirchild3/": H{},
			"filechild1": nil,
		},
	}
	_, err := createTree(origtree, consts.RootDirID)
	if !assert.NoError(t, err) {
		return
	}
	err = vfs.RemoveAll(fs, "/removemeall", fs.EnsureErased)
	if !assert.NoError(t, err) {
		return
	}
	_, err = fs.DirByPath("/removemeall/dirchild1")
	assert.Error(t, err)
	_, err = fs.DirByPath("/removemeall")
	assert.Error(t, err)
}

func TestDiskUsage(t *testing.T) {
	used, err := fs.DiskUsage()
	assert.NoError(t, err)
	assert.Equal(t, len("hello !"), int(used))
}

func TestGetFileDocFromPath(t *testing.T) {
	dir, _ := vfs.NewDirDoc(fs, "container", "", nil)
	err := fs.CreateDir(dir)
	assert.NoError(t, err)

	doc, err := vfs.NewFileDoc("toto", dir.ID(), -1, nil, "foo/bar", "foo", time.Now(), false, false, false, []string{})
	assert.NoError(t, err)

	body := bytes.NewReader([]byte("hello !"))

	file, err := fs.CreateFile(doc, nil)
	assert.NoError(t, err)

	n, err := io.Copy(file, body)
	assert.NoError(t, err)
	assert.Equal(t, len("hello !"), int(n))

	err = file.Close()
	assert.NoError(t, err)

	_, err = fs.FileByPath("/container/toto")
	assert.NoError(t, err)

	_, err = fs.FileByPath("/container/noooo")
	assert.Error(t, err)
}

func TestCreateGetAndModifyFile(t *testing.T) {
	origtree := H{
		"createandget1/": H{
			"dirchild1/": H{
				"food/": H{},
				"bard/": H{},
			},
			"dirchild2/": H{
				"foof": nil,
				"barf": nil,
			},
			"dirchild3/": H{},
			"filechild1": nil,
		},
	}

	olddoc, err := createTree(origtree, consts.RootDirID)

	if !assert.NoError(t, err) {
		return
	}

	newname := "createandget2"
	_, err = vfs.ModifyDirMetadata(fs, olddoc, &vfs.DocPatch{
		Name: &newname,
	})
	if !assert.NoError(t, err) {
		return
	}

	tree, err := fetchTree("/createandget2")
	if !assert.NoError(t, err) {
		return
	}

	assert.EqualValues(t, origtree["createandget1/"], tree["createandget2/"], "should have same tree")

	fileBefore, err := fs.FileByPath("/createandget2/dirchild2/foof")
	if !assert.NoError(t, err) {
		return
	}
	newfilename := "foof.jpg"
	_, err = vfs.ModifyFileMetadata(fs, fileBefore, &vfs.DocPatch{
		Name: &newfilename,
	})
	if !assert.NoError(t, err) {
		return
	}
	fileAfter, err := fs.FileByPath("/createandget2/dirchild2/foof.jpg")
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, "files", fileBefore.Class)
	assert.Equal(t, "application/octet-stream", fileBefore.Mime)
	assert.Equal(t, "image", fileAfter.Class)
	assert.Equal(t, "image/jpeg", fileAfter.Mime)
}

func TestUpdateDir(t *testing.T) {
	origtree := H{
		"update1/": H{
			"dirchild1/": H{
				"food/": H{},
				"bard/": H{},
			},
			"dirchild2/": H{
				"foof": nil,
				"barf": nil,
			},
			"dirchild3/": H{},
			"filechild1": nil,
		},
	}

	doc1, err := createTree(origtree, consts.RootDirID)
	if !assert.NoError(t, err) {
		return
	}

	newname := "update2"
	_, err = vfs.ModifyDirMetadata(fs, doc1, &vfs.DocPatch{
		Name: &newname,
	})
	if !assert.NoError(t, err) {
		return
	}

	tree, err := fetchTree("/update2")
	if !assert.NoError(t, err) {
		return
	}

	if !assert.EqualValues(t, origtree["update1/"], tree["update2/"], "should have same tree") {
		return
	}

	dirchild2, err := fs.DirByPath("/update2/dirchild2")
	if !assert.NoError(t, err) {
		return
	}

	dirchild3, err := fs.DirByPath("/update2/dirchild3")
	if !assert.NoError(t, err) {
		return
	}

	newfolid := dirchild2.ID()
	_, err = vfs.ModifyDirMetadata(fs, dirchild3, &vfs.DocPatch{
		DirID: &newfolid,
	})
	if !assert.NoError(t, err) {
		return
	}

	tree, err = fetchTree("/update2")
	if !assert.NoError(t, err) {
		return
	}

	assert.EqualValues(t, H{
		"update2/": H{
			"dirchild1/": H{
				"bard/": H{},
				"food/": H{},
			},
			"filechild1": nil,
			"dirchild2/": H{
				"barf":       nil,
				"foof":       nil,
				"dirchild3/": H{},
			},
		},
	}, tree)
}

func TestEncodingOfDirName(t *testing.T) {
	base := "encoding-dir"
	nfc := "chaîne"
	nfd := "chaîne"

	origtree := H{base + "/": H{
		nfc: H{},
		nfd: H{},
	}}
	_, err := createTree(origtree, consts.RootDirID)
	require.NoError(t, err)

	f1, err := fs.FileByPath("/" + base + "/" + nfc)
	require.NoError(t, err)
	assert.Equal(t, nfc, f1.DocName)

	f2, err := fs.FileByPath("/" + base + "/" + nfd)
	require.NoError(t, err)
	assert.Equal(t, nfd, f2.DocName)

	assert.NotEqual(t, f1.DocID, f2.DocID)
}

func TestChangeEncodingOfDirName(t *testing.T) {
	nfc := "dir-nfc-to-nfd-é"
	nfd := "dir-nfc-to-nfd-é"

	origtree := H{nfc + "/": H{
		"dirchild1/": H{
			"food/": H{},
			"bard/": H{},
		},
		"dirchild2/": H{},
		"filechild1": nil,
	}}
	doc, err := createTree(origtree, consts.RootDirID)
	require.NoError(t, err)

	newname := nfd
	doc, err = vfs.ModifyDirMetadata(fs, doc, &vfs.DocPatch{
		Name: &newname,
	})
	require.NoError(t, err)
	d, err := fs.DirByPath("/" + newname)
	require.NoError(t, err)
	assert.Equal(t, newname, d.DocName)

	newname = nfc
	_, err = vfs.ModifyDirMetadata(fs, doc, &vfs.DocPatch{
		Name: &newname,
	})
	require.NoError(t, err)
	d, err = fs.DirByPath("/" + newname)
	require.NoError(t, err)
	assert.Equal(t, newname, d.DocName)
}

func TestWalk(t *testing.T) {
	walktree := H{
		"walk/": H{
			"dirchild1/": H{
				"food/": H{},
				"bard/": H{},
			},
			"dirchild2/": H{
				"foof": nil,
				"barf": nil,
			},
			"dirchild3/": H{},
			"filechild1": nil,
		},
	}

	_, err := createTree(walktree, consts.RootDirID)
	if !assert.NoError(t, err) {
		return
	}

	walked := H{}
	err = vfs.Walk(fs, "/walk", func(name string, dir *vfs.DirDoc, file *vfs.FileDoc, err error) error {
		if !assert.NoError(t, err) {
			return err
		}

		if dir != nil && !assert.Equal(t, dir.Fullpath, name) {
			return fmt.Errorf("Bad fullpath")
		}

		if file != nil && !assert.True(t, strings.HasSuffix(name, file.DocName)) {
			return fmt.Errorf("Bad fullpath")
		}

		walked[name] = nil
		return nil
	})
	assert.NoError(t, err)

	expectedWalk := H{
		"/walk":                nil,
		"/walk/dirchild1":      nil,
		"/walk/dirchild1/food": nil,
		"/walk/dirchild1/bard": nil,
		"/walk/dirchild2":      nil,
		"/walk/dirchild2/foof": nil,
		"/walk/dirchild2/barf": nil,
		"/walk/dirchild3":      nil,
		"/walk/filechild1":     nil,
	}

	assert.Equal(t, expectedWalk, walked)
}

func TestWalkAlreadyLocked(t *testing.T) {
	walktree := H{
		"walk2/": H{
			"dirchild1/": H{
				"food/": H{},
				"bard/": H{},
			},
			"dirchild2/": H{
				"foof": nil,
				"barf": nil,
			},
			"dirchild3/": H{},
			"filechild1": nil,
		},
	}

	_, err := createTree(walktree, consts.RootDirID)
	if !assert.NoError(t, err) {
		return
	}

	done := make(chan bool)

	go func() {
		dir, err := fs.DirByPath("/walk2")
		assert.NoError(t, err)

		assert.NoError(t, mutex.Lock())
		defer mutex.Unlock()

		err = vfs.WalkAlreadyLocked(fs, dir, func(_ string, _ *vfs.DirDoc, _ *vfs.FileDoc, err error) error {
			assert.NoError(t, err)
			return err
		})
		assert.NoError(t, err)
		done <- true
	}()

	select {
	case <-done:
		return
	case <-time.After(3 * time.Second):
		panic(errors.New("deadline: WalkAlreadyLocked is probably trying to acquire the VFS lock"))
	}
}

func TestContentDisposition(t *testing.T) {
	foo := vfs.ContentDisposition("inline", "foo.jpg")
	assert.Equal(t, `inline; filename="foo.jpg"`, foo)
	space := vfs.ContentDisposition("inline", "foo bar.jpg")
	assert.Equal(t, `inline; filename="foobar.jpg"; filename*=UTF-8''foo%20bar.jpg`, space)
	accents := vfs.ContentDisposition("inline", "héçà")
	assert.Equal(t, `inline; filename="h"; filename*=UTF-8''h%C3%A9%C3%A7%C3%A0`, accents)
	tab := vfs.ContentDisposition("inline", "tab\t")
	assert.Equal(t, `inline; filename="tab"; filename*=UTF-8''tab%09`, tab)
	emoji := vfs.ContentDisposition("inline", "🐧")
	assert.Equal(t, `inline; filename="download"; filename*=UTF-8''%F0%9F%90%A7`, emoji)
}

func TestArchive(t *testing.T) {
	tree := H{
		"archive/": H{
			"foo.jpg":    nil,
			"foobar.jpg": nil,
			"hello.jpg":  nil,
			"bar/": H{
				"baz/": H{
					"one.png": nil,
					"two.png": nil,
				},
				"z.gif": nil,
			},
			"qux/": H{
				"quux":   nil,
				"courge": nil,
			},
		},
	}
	dirdoc, err := createTree(tree, consts.RootDirID)
	assert.NoError(t, err)

	foobar, err := fs.FileByPath("/archive/foobar.jpg")
	assert.NoError(t, err)

	a := &vfs.Archive{
		Name: "test",
		IDs: []string{
			foobar.ID(),
		},
		Files: []string{
			"/archive/foo.jpg",
			"/archive/bar",
		},
	}
	w := httptest.NewRecorder()
	err = a.Serve(fs, w)
	assert.NoError(t, err)

	res := w.Result()
	disposition := res.Header.Get("Content-Disposition")
	assert.Equal(t, `attachment; filename="test.zip"`, disposition)
	assert.Equal(t, "application/zip", res.Header.Get("Content-Type"))

	b, err := ioutil.ReadAll(res.Body)
	assert.NoError(t, err)
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	assert.NoError(t, err)
	assert.Equal(t, 7, len(z.File))
	zipfiles := H{}
	for _, f := range z.File {
		zipfiles[f.Name] = nil
	}
	assert.EqualValues(t, H{
		"test/foobar.jpg":      nil,
		"test/foo.jpg":         nil,
		"test/bar/":            nil,
		"test/bar/baz/":        nil,
		"test/bar/baz/one.png": nil,
		"test/bar/baz/two.png": nil,
		"test/bar/z.gif":       nil,
	}, zipfiles)
	assert.NoError(t, fs.DestroyDirAndContent(dirdoc, fs.EnsureErased))
}

func TestCreateFileTooBig(t *testing.T) {
	diskQuota = 1 << (1 * 10) // 1KB
	defer func() { diskQuota = 0 }()

	diskUsage1, err := fs.DiskUsage()
	if !assert.NoError(t, err) {
		return
	}

	doc1, err := vfs.NewFileDoc(
		"too-big",
		consts.RootDirID,
		diskQuota+1,
		nil,
		"application/octet-stream",
		"application",
		time.Now(),
		false,
		false,
		false,
		nil,
	)
	if !assert.NoError(t, err) {
		return
	}
	_, err = fs.CreateFile(doc1, nil)
	assert.Equal(t, vfs.ErrFileTooBig, err)

	doc2, err := vfs.NewFileDoc(
		"too-big",
		consts.RootDirID,
		diskQuota/2,
		nil,
		"application/octet-stream",
		"application",
		time.Now(),
		false,
		false,
		false,
		nil,
	)
	if !assert.NoError(t, err) {
		return
	}
	f, err := fs.CreateFile(doc2, nil)
	assert.NoError(t, err)
	assert.Error(t, f.Close())

	_, err = fs.FileByPath("/too-big")
	assert.True(t, os.IsNotExist(err))

	doc3, err := vfs.NewFileDoc(
		"too-big",
		consts.RootDirID,
		diskQuota/2,
		nil,
		"application/octet-stream",
		"application",
		time.Now(),
		false,
		false,
		false,
		nil,
	)
	if !assert.NoError(t, err) {
		return
	}
	f, err = fs.CreateFile(doc3, nil)
	assert.NoError(t, err)
	_, err = io.Copy(f, bytes.NewReader(crypto.GenerateRandomBytes(int(doc3.ByteSize))))
	assert.NoError(t, err)
	err = f.Close()
	assert.NoError(t, err)

	diskUsage2, err := fs.DiskUsage()
	assert.NoError(t, err)
	assert.Equal(t, diskUsage1+diskQuota/2, diskUsage2)

	doc4, err := vfs.NewFileDoc(
		"too-big2",
		consts.RootDirID,
		-1,
		nil,
		"application/octet-stream",
		"application",
		time.Now(),
		false,
		false,
		false,
		nil,
	)
	if !assert.NoError(t, err) {
		return
	}
	f, err = fs.CreateFile(doc4, nil)
	assert.NoError(t, err)
	_, err = io.Copy(f, bytes.NewReader(crypto.GenerateRandomBytes(int(diskQuota/2+1))))
	assert.Error(t, err)
	assert.Equal(t, vfs.ErrFileTooBig, err)
	err = f.Close()
	assert.Error(t, err)
	assert.Equal(t, vfs.ErrFileTooBig, err)

	_, err = fs.FileByPath("/too-big2")
	assert.True(t, os.IsNotExist(err))

	root, err := fs.DirByPath("/")
	if !assert.NoError(t, err) {
		return
	}
	assert.NoError(t, fs.DestroyDirContent(root, fs.EnsureErased))
}

func TestCreateFileDocCopy(t *testing.T) {
	md5sum := []byte("md5sum")
	file, err := vfs.NewFileDoc("file", consts.RootDirID, -1, md5sum, "foo/bar", "foo", time.Now(), false, false, false, []string{})
	require.NoError(t, err)

	newname := "file (copy)"
	newdoc := vfs.CreateFileDocCopy(file, newname)
	assert.Empty(t, newdoc.DocID)
	assert.Empty(t, newdoc.DocRev)
	assert.Equal(t, newname, newdoc.DocName)
	assert.Equal(t, file.DirID, newdoc.DirID)
	assert.Equal(t, file.ByteSize, newdoc.ByteSize)
	assert.Equal(t, file.MD5Sum, newdoc.MD5Sum)
	assert.NotEqual(t, file.CreatedAt, newdoc.CreatedAt)
	assert.Empty(t, newdoc.ReferencedBy)
}

func TestConflictName(t *testing.T) {
	tree := H{"existing": nil}
	_, err := createTree(tree, consts.RootDirID)
	require.NoError(t, err)

	newname := vfs.ConflictName(fs, consts.RootDirID, "existing", true)
	assert.Equal(t, "existing (2)", newname)

	tree = H{"existing (2)": nil}
	_, err = createTree(tree, consts.RootDirID)
	require.NoError(t, err)

	newname = vfs.ConflictName(fs, consts.RootDirID, "existing", true)
	assert.Equal(t, "existing (3)", newname)

	tree = H{"existing (3)": nil}
	_, err = createTree(tree, consts.RootDirID)
	require.NoError(t, err)

	newname = vfs.ConflictName(fs, consts.RootDirID, "existing (3)", true)
	assert.Equal(t, "existing (4)", newname)

	tree = H{"existing (copy)": nil}
	_, err = createTree(tree, consts.RootDirID)
	require.NoError(t, err)

	newname = vfs.ConflictName(fs, consts.RootDirID, "existing (copy)", true)
	assert.Equal(t, "existing (copy) (2)", newname)
}

func TestCheckAvailableSpace(t *testing.T) {
	diskQuota = 0

	doc, err := vfs.NewFileDoc("toto", consts.RootDirID, 100, nil, "foo/bar", "foo", time.Now(), false, false, false, []string{})
	require.NoError(t, err)
	_, _, _, err = vfs.CheckAvailableDiskSpace(fs, doc)
	require.NoError(t, err)

	diskQuota = 100

	doc, err = vfs.NewFileDoc("toto", consts.RootDirID, 100, nil, "foo/bar", "foo", time.Now(), false, false, false, []string{})
	require.NoError(t, err)
	_, _, _, err = vfs.CheckAvailableDiskSpace(fs, doc)
	require.NoError(t, err)

	doc, err = vfs.NewFileDoc("toto", consts.RootDirID, 101, nil, "foo/bar", "foo", time.Now(), false, false, false, []string{})
	require.NoError(t, err)
	_, _, _, err = vfs.CheckAvailableDiskSpace(fs, doc)
	assert.Error(t, err)
	assert.Equal(t, vfs.ErrFileTooBig, err)

	maxFileSize := fs.MaxFileSize()
	if maxFileSize > 0 {
		doc, err = vfs.NewFileDoc("toto", consts.RootDirID, maxFileSize+1, nil, "foo/bar", "foo", time.Now(), false, false, false, []string{})
		require.NoError(t, err)
		_, _, _, err = vfs.CheckAvailableDiskSpace(fs, doc)
		assert.Error(t, err)
		assert.Equal(t, vfs.ErrFileTooBig, err)
	}
}

func TestMain(m *testing.M) {
	config.UseTestFile()

	_, err := couchdb.CheckStatus()
	if err != nil {
		fmt.Println("This test need couchdb to run.")
		os.Exit(1)
	}

	var rollback func()
	fs, rollback, err = makeAferoFS()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	res1 := m.Run()
	rollback()

	fs, rollback, err = makeSwiftFS(2)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	res2 := m.Run()
	rollback()

	os.Exit(res1 + res2)
}

type contexter struct {
	cluster int
	domain  string
	prefix  string
	context string
}

func (c *contexter) DBCluster() int         { return c.cluster }
func (c *contexter) DomainName() string     { return c.domain }
func (c *contexter) DBPrefix() string       { return c.prefix }
func (c *contexter) GetContextName() string { return c.context }

func makeAferoFS() (vfs.VFS, func(), error) {
	tempdir, err := ioutil.TempDir("", "cozy-stack")
	if err != nil {
		return nil, nil, errors.New("could not create temporary directory")
	}

	db := &contexter{0, "io.cozy.vfs.test", "io.cozy.vfs.test", "cozy_beta"}
	index := vfs.NewCouchdbIndexer(db)
	mutex = lock.ReadWrite(db, "vfs-afero-test")
	aferoFs, err := vfsafero.New(db, index, &diskImpl{}, mutex,
		&url.URL{Scheme: "file", Host: "localhost", Path: tempdir}, "io.cozy.vfs.test")
	if err != nil {
		return nil, nil, err
	}

	err = couchdb.ResetDB(db, consts.Files)
	if err != nil {
		return nil, nil, err
	}

	g, _ := errgroup.WithContext(context.Background())
	couchdb.DefineIndexes(g, db, couchdb.IndexesByDoctype(consts.Files))
	couchdb.DefineViews(g, db, couchdb.ViewsByDoctype(consts.Files))
	if err = g.Wait(); err != nil {
		return nil, nil, err
	}

	err = aferoFs.InitFs()
	if err != nil {
		return nil, nil, err
	}

	return aferoFs, func() {
		_ = os.RemoveAll(tempdir)
		_ = couchdb.DeleteDB(db, consts.Files)
	}, nil
}

func makeSwiftFS(layout int) (vfs.VFS, func(), error) {
	db := &contexter{0, "io.cozy.vfs.test", "io.cozy.vfs.test", "cozy_beta"}
	index := vfs.NewCouchdbIndexer(db)
	swiftSrv, err := swifttest.NewSwiftServer("localhost")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create swift server %s", err)
	}

	err = config.InitSwiftConnection(config.Fs{
		URL: &url.URL{
			Scheme:   "swift",
			Host:     "localhost",
			RawQuery: "UserName=swifttest&Password=swifttest&AuthURL=" + url.QueryEscape(swiftSrv.AuthURL),
		},
	})
	if err != nil {
		return nil, nil, err
	}

	var swiftFs vfs.VFS
	switch layout {
	case 0:
		mutex = lock.ReadWrite(db, "vfs-swift-test")
		swiftFs, err = vfsswift.New(db, index, &diskImpl{}, mutex)
	case 1:
		mutex = lock.ReadWrite(db, "vfs-swiftv2-test")
		swiftFs, err = vfsswift.NewV2(db, index, &diskImpl{}, mutex)
	case 2:
		mutex = lock.ReadWrite(db, "vfs-swiftv3-test")
		swiftFs, err = vfsswift.NewV3(db, index, &diskImpl{}, mutex)
	}
	if err != nil {
		return nil, nil, err
	}

	err = couchdb.ResetDB(db, consts.Files)
	if err != nil {
		return nil, nil, err
	}

	g, _ := errgroup.WithContext(context.Background())
	couchdb.DefineIndexes(g, db, couchdb.IndexesByDoctype(consts.Files))
	couchdb.DefineViews(g, db, couchdb.ViewsByDoctype(consts.Files))
	if err = g.Wait(); err != nil {
		return nil, nil, err
	}

	err = swiftFs.InitFs()
	if err != nil {
		return nil, nil, err
	}

	return swiftFs, func() {
		_ = couchdb.DeleteDB(db, consts.Files)
		if swiftSrv != nil {
			swiftSrv.Close()
		}
	}, nil
}
