package middlewares

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cozy/cozy-stack/pkg/assets/dynamic"
	"github.com/cozy/cozy-stack/pkg/config/config"
	"github.com/cozy/cozy-stack/tests/testutils"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCors(t *testing.T) {
	if testing.Short() {
		t.Skip("an instance is required for this test: test skipped due to the use of --short flag")
	}

	config.UseTestFile()
	config.GetConfig().Assets = "../../assets"
	setup := testutils.NewSetup(t, t.Name())

	require.NoError(t, setup.SetupSwiftTest(), "Could not init Swift test")
	require.NoError(t, dynamic.InitDynamicAssetFS(config.FsURL().String()), "Could not init dynamic FS")

	t.Run("CORSMiddleware", func(t *testing.T) {
		e := echo.New()
		req, _ := http.NewRequest(echo.OPTIONS, "http://cozy.local/data/io.cozy.files", nil)
		req.Header.Set("Origin", "fakecozy.local")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		h := CORS(CORSOptions{})(echo.NotFoundHandler)
		_ = h(c)
		assert.Equal(t, "fakecozy.local", rec.Header().Get(echo.HeaderAccessControlAllowOrigin))
	})

	t.Run("CORSMiddlewareNotAuth", func(t *testing.T) {
		e := echo.New()
		req, _ := http.NewRequest(echo.OPTIONS, "http://cozy.local/auth/register", nil)
		req.Header.Set("Origin", "fakecozy.local")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetPath(req.URL.Path)
		h := CORS(CORSOptions{BlockList: []string{"/auth/"}})(echo.NotFoundHandler)
		_ = h(c)
		assert.Equal(t, "", rec.Header().Get(echo.HeaderAccessControlAllowOrigin))
	})
}
